package models

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const (
	MaxDeliveryPlanStepActivityExecutableName = 24
	MaxDeliveryPlanStepActivityArgumentCount  = 128
	MaxDeliveryPlanStepActivityOutputBytes    = 24000
	MaxDeliveryPlanStepActivityDetailsBytes   = 4096
	MaxDeliveryPlanStepActivityResourceRefs   = 16
	MaxDeliveryPlanStepActivityChangedFiles   = 50
	MaxDeliveryPlanStepActivityPathBytes      = 256
	MaxDeliveryPlanStepPatchArtifacts         = 6
	MaxDeliveryPlanStepPatchArtifactBytes     = 4 * 1024 * 1024
)

// DeliveryPlanStepActivityDetails is the only structured detail payload that
// may accompany terminal activity events. It intentionally has no field for
// command text, argv values, file contents, output text, or caller-authored
// descriptions.
type DeliveryPlanStepActivityDetails struct {
	ExecutableName                  string                                   `json:"executable_name,omitempty"`
	ArgumentCount                   *int                                     `json:"argument_count,omitempty"`
	ExitCode                        *int                                     `json:"exit_code,omitempty"`
	CapturedOutputBytes             *int                                     `json:"captured_output_bytes,omitempty"`
	ResourceReferences              []string                                 `json:"resource_references,omitempty"`
	ChangedFiles                    []string                                 `json:"changed_files,omitempty"`
	AcceptanceChecks                []DeliveryPlanStepAcceptanceCheck        `json:"acceptance_checks,omitempty"`
	ReviewDiffSHA256                string                                   `json:"review_diff_sha256,omitempty"`
	PatchArtifacts                  []DeliveryPlanStepPatchArtifactReference `json:"patch_artifacts,omitempty"`
	AppliedDependencyManifestSHA256 string                                   `json:"applied_dependency_manifest_sha256,omitempty"`
	AppliedDependencyPatchCount     *int                                     `json:"applied_dependency_patch_count,omitempty"`
}

// DeliveryPlanStepPatchArtifactReference describes a patch already uploaded
// to the task/run-scoped private output bucket. It contains no storage key or
// patch bytes; the API derives the object key from the authenticated callback.
type DeliveryPlanStepPatchArtifactReference struct {
	RepositoryRef string `json:"repository_ref"`
	BaseSHA       string `json:"base_sha"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
}

// DeliveryPlanStepAcceptanceCheck carries only a digest of the approved
// criterion and the worker's pass attestation. Criterion text and check output
// deliberately have no representation in the append-only activity event.
type DeliveryPlanStepAcceptanceCheck struct {
	CriterionSHA256 string `json:"criterion_sha256"`
	Passed          bool   `json:"passed"`
}

var deliveryPlanStepActivityExecutables = map[string]struct{}{
	"cargo": {}, "docker": {}, "dotnet": {}, "git": {}, "go": {}, "make": {},
	"node": {}, "npm": {}, "npx": {}, "powershell": {}, "pwsh": {},
	"pytest": {}, "python": {}, "python3": {}, "pnpm": {}, "yarn": {},
}

// ValidateDeliveryPlanStepActivityDetails enforces a strict allow-list and
// bounded values for the typed terminal-event detail projection.
func ValidateDeliveryPlanStepActivityDetails(action, phase string, details *DeliveryPlanStepActivityDetails) error {
	if details == nil {
		return nil
	}
	if phase == DeliveryPlanStepActivityStarted {
		return fmt.Errorf("started activity cannot contain details")
	}
	if phase != DeliveryPlanStepActivityCompleted && phase != DeliveryPlanStepActivityFailed {
		return fmt.Errorf("activity details require a terminal phase")
	}
	commandFields := details.ExecutableName != "" || details.ArgumentCount != nil || details.ExitCode != nil || details.CapturedOutputBytes != nil
	patchArtifacts := len(details.PatchArtifacts) > 0
	dependencyManifestFields := details.AppliedDependencyManifestSHA256 != "" || details.AppliedDependencyPatchCount != nil
	if dependencyManifestFields {
		if action != DeliveryPlanStepActivityEvidence || phase != DeliveryPlanStepActivityCompleted || !validDeliveryPlanStepSHA256(details.AppliedDependencyManifestSHA256) || details.AppliedDependencyPatchCount == nil || *details.AppliedDependencyPatchCount < 0 || *details.AppliedDependencyPatchCount > MaxDeliveryPlanStepDependencyPatchReferences {
			return fmt.Errorf("applied dependency manifest is invalid")
		}
	}
	acceptanceFields := len(details.AcceptanceChecks) > 0 || (details.ReviewDiffSHA256 != "" && !patchArtifacts)
	if patchArtifacts {
		if action != DeliveryPlanStepActivityEvidence || phase != DeliveryPlanStepActivityCompleted || len(details.PatchArtifacts) > MaxDeliveryPlanStepPatchArtifacts {
			return fmt.Errorf("patch artifacts are not allowed for this activity")
		}
		seenRepositories := make(map[string]struct{}, len(details.PatchArtifacts))
		for _, artifact := range details.PatchArtifacts {
			if !validDeliveryPlanStepPatchRepositoryReference(artifact.RepositoryRef) || !validDeliveryPlanStepCommitSHA(artifact.BaseSHA) || !validDeliveryPlanStepSHA256(artifact.SHA256) || artifact.SizeBytes < 1 || artifact.SizeBytes > MaxDeliveryPlanStepPatchArtifactBytes {
				return fmt.Errorf("patch artifact reference is invalid")
			}
			if _, duplicate := seenRepositories[artifact.RepositoryRef]; duplicate {
				return fmt.Errorf("patch artifact repositories must be unique")
			}
			seenRepositories[artifact.RepositoryRef] = struct{}{}
		}
		manifestSHA256, err := DeliveryPlanStepPatchArtifactManifestSHA256(details.PatchArtifacts)
		if err != nil || !validDeliveryPlanStepSHA256(details.ReviewDiffSHA256) || details.ReviewDiffSHA256 != manifestSHA256 {
			return fmt.Errorf("review diff digest does not match patch artifact manifest")
		}
	}
	if acceptanceFields {
		if action != DeliveryPlanStepActivityEvidence || phase != DeliveryPlanStepActivityCompleted || len(details.AcceptanceChecks) == 0 || !validDeliveryPlanStepSHA256(details.ReviewDiffSHA256) {
			return fmt.Errorf("acceptance evidence is incomplete or not allowed for this activity")
		}
		if commandFields || len(details.ResourceReferences) > 0 || len(details.ChangedFiles) > 0 {
			return fmt.Errorf("acceptance evidence cannot include other activity details")
		}
		seenCriteria := make(map[string]struct{}, len(details.AcceptanceChecks))
		for _, check := range details.AcceptanceChecks {
			if !validDeliveryPlanStepSHA256(check.CriterionSHA256) || !check.Passed {
				return fmt.Errorf("acceptance evidence must contain passing criterion digests")
			}
			if _, duplicate := seenCriteria[check.CriterionSHA256]; duplicate {
				return fmt.Errorf("acceptance criterion digests must be unique")
			}
			seenCriteria[check.CriterionSHA256] = struct{}{}
		}
	}
	if commandFields {
		if action != DeliveryPlanStepActivityCommand && action != DeliveryPlanStepActivityValidation {
			return fmt.Errorf("command details are not allowed for this action")
		}
		if _, allowed := deliveryPlanStepActivityExecutables[details.ExecutableName]; !allowed || details.ArgumentCount == nil || details.ExitCode == nil || details.CapturedOutputBytes == nil {
			return fmt.Errorf("command details are incomplete or not allow-listed")
		}
		if *details.ArgumentCount < 0 || *details.ArgumentCount > MaxDeliveryPlanStepActivityArgumentCount || *details.ExitCode < 0 || *details.ExitCode > 255 || *details.CapturedOutputBytes < 0 || *details.CapturedOutputBytes > MaxDeliveryPlanStepActivityOutputBytes {
			return fmt.Errorf("command details exceed the allowed bounds")
		}
	}
	if len(details.ResourceReferences) > MaxDeliveryPlanStepActivityResourceRefs {
		return fmt.Errorf("too many resource references")
	}
	if len(details.ResourceReferences) > 0 && action != DeliveryPlanStepActivityFileRead && action != DeliveryPlanStepActivityFileChange && action != DeliveryPlanStepActivityCommand && action != DeliveryPlanStepActivityValidation && action != DeliveryPlanStepActivityEvidence {
		return fmt.Errorf("resource references are not allowed for this action")
	}
	seenRefs := make(map[string]struct{}, len(details.ResourceReferences))
	for _, reference := range details.ResourceReferences {
		if !validDeliveryPlanStepActivityResourceReference(reference) {
			return fmt.Errorf("resource reference is invalid")
		}
		if _, duplicate := seenRefs[reference]; duplicate {
			return fmt.Errorf("resource references must be unique")
		}
		seenRefs[reference] = struct{}{}
	}
	if len(details.ChangedFiles) > MaxDeliveryPlanStepActivityChangedFiles {
		return fmt.Errorf("too many changed files")
	}
	if len(details.ChangedFiles) > 0 && action != DeliveryPlanStepActivityFileChange {
		return fmt.Errorf("changed files are not allowed for this action")
	}
	seenFiles := make(map[string]struct{}, len(details.ChangedFiles))
	for _, file := range details.ChangedFiles {
		if !validDeliveryPlanStepActivityRelativePath(file) {
			return fmt.Errorf("changed file path is invalid")
		}
		if _, duplicate := seenFiles[file]; duplicate {
			return fmt.Errorf("changed files must be unique")
		}
		seenFiles[file] = struct{}{}
	}
	if !commandFields && len(details.ResourceReferences) == 0 && len(details.ChangedFiles) == 0 && !acceptanceFields && !patchArtifacts && !dependencyManifestFields {
		return fmt.Errorf("activity details are empty")
	}
	return nil
}

// DeliveryPlanStepPatchArtifactManifestSHA256 computes the canonical manifest
// digest shared by worker and backend. Repositories are sorted by RepositoryRef
// and each row is newline-delimited in repository/base/digest/size order.
func DeliveryPlanStepPatchArtifactManifestSHA256(artifacts []DeliveryPlanStepPatchArtifactReference) (string, error) {
	if len(artifacts) == 0 || len(artifacts) > MaxDeliveryPlanStepPatchArtifacts {
		return "", fmt.Errorf("patch artifact manifest size is invalid")
	}
	ordered := append([]DeliveryPlanStepPatchArtifactReference(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RepositoryRef < ordered[j].RepositoryRef })
	var manifest strings.Builder
	seenRepositories := make(map[string]struct{}, len(ordered))
	for _, artifact := range ordered {
		if !validDeliveryPlanStepPatchRepositoryReference(artifact.RepositoryRef) || !validDeliveryPlanStepCommitSHA(artifact.BaseSHA) || !validDeliveryPlanStepSHA256(artifact.SHA256) || artifact.SizeBytes < 1 || artifact.SizeBytes > MaxDeliveryPlanStepPatchArtifactBytes {
			return "", fmt.Errorf("patch artifact manifest contains an invalid reference")
		}
		if _, duplicate := seenRepositories[artifact.RepositoryRef]; duplicate {
			return "", fmt.Errorf("patch artifact manifest contains a duplicate repository")
		}
		seenRepositories[artifact.RepositoryRef] = struct{}{}
		manifest.WriteString(artifact.RepositoryRef)
		manifest.WriteByte('\n')
		manifest.WriteString(artifact.BaseSHA)
		manifest.WriteByte('\n')
		manifest.WriteString(artifact.SHA256)
		manifest.WriteByte('\n')
		manifest.WriteString(strconv.FormatInt(artifact.SizeBytes, 10))
		manifest.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(manifest.String()))
	return hex.EncodeToString(digest[:]), nil
}

func validDeliveryPlanStepCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validDeliveryPlanStepPatchRepositoryReference(reference string) bool {
	if !validDeliveryPlanStepActivityResourceReference(reference) || !strings.HasPrefix(reference, "workspace://") {
		return false
	}
	return !strings.Contains(strings.TrimPrefix(reference, "workspace://"), "/")
}

// IsSafeDeliveryPlanStepPatchRepositoryReference accepts only an opaque root
// workspace repository reference, never a path within that workspace.
func IsSafeDeliveryPlanStepPatchRepositoryReference(reference string) bool {
	return validDeliveryPlanStepPatchRepositoryReference(reference)
}

func validDeliveryPlanStepSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validDeliveryPlanStepActivityResourceReference(reference string) bool {
	if len(reference) < len("workspace://x") || len(reference) > MaxDeliveryPlanStepActivityPathBytes || !strings.HasPrefix(reference, "workspace://") || strings.ContainsAny(reference, "\\?#%\x00\r\n") {
		return false
	}
	remainder := strings.TrimPrefix(reference, "workspace://")
	parts := strings.SplitN(remainder, "/", 2)
	workspaceID := parts[0]
	if workspaceID == "" || workspaceID == "." || workspaceID == ".." || len(workspaceID) > 96 {
		return false
	}
	for _, char := range workspaceID {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.'
		if !valid {
			return false
		}
	}
	return len(parts) == 1 || validDeliveryPlanStepActivityRelativePath(parts[1])
}

func validDeliveryPlanStepActivityRelativePath(relative string) bool {
	if relative == "" || len(relative) > MaxDeliveryPlanStepActivityPathBytes || strings.ContainsAny(relative, "\\:\x00\r\n") || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return false
	}
	for _, segment := range strings.Split(relative, "/") {
		lower := strings.ToLower(segment)
		if segment == "" || segment == "." || segment == ".." || lower == ".git" || strings.HasPrefix(lower, ".env") {
			return false
		}
		switch lower {
		case ".aws", ".docker", ".gcloud", ".kube", ".ssh", ".npmrc", ".pypirc", ".netrc", ".dockercfg", ".htpasswd", ".pgpass", "kubeconfig", "terraform.tfstate":
			return false
		}
		if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") || strings.HasPrefix(lower, "id_ecdsa") || strings.HasPrefix(lower, "id_dsa") {
			return false
		}
		for _, sensitive := range []string{"credential", "secret", "private_key", "api_key", "apikey", "access_key", "token", "password", "service_account"} {
			if strings.Contains(lower, sensitive) {
				return false
			}
		}
		extension := strings.ToLower(path.Ext(segment))
		switch extension {
		case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".cer", ".crt", ".der":
			return false
		}
		for _, char := range segment {
			if char < 0x20 || char == 0x7f {
				return false
			}
		}
	}
	return true
}

// IsSafeDeliveryPlanStepPatchPath applies the same traversal and credential-file
// denylist used by activity paths to a path parsed from an uploaded Git patch.
func IsSafeDeliveryPlanStepPatchPath(relative string) bool {
	return validDeliveryPlanStepActivityRelativePath(relative)
}

const (
	DeliveryPlanStepActivityInference  = "inference"
	DeliveryPlanStepActivityTool       = "tool"
	DeliveryPlanStepActivityFileRead   = "file_read"
	DeliveryPlanStepActivityFileChange = "file_change"
	DeliveryPlanStepActivityCommand    = "command"
	DeliveryPlanStepActivityValidation = "validation"
	DeliveryPlanStepActivityEvidence   = "evidence"

	DeliveryPlanStepActivityStarted   = "started"
	DeliveryPlanStepActivityCompleted = "completed"
	DeliveryPlanStepActivityFailed    = "failed"
)

// DeliveryPlanStepActivityEvent is a sanitized, append-only observable action
// emitted by the worker while it owns a plan-step lease. Its only detail field
// is a strictly typed, bounded projection; prompts, model reasoning, raw
// arguments/results, command lines, and file contents are never represented.
// Event IDs and per-run sequence numbers make retries idempotent.
type DeliveryPlanStepActivityEvent struct {
	ID                 uuid.UUID  `gorm:"type:uuid;primaryKey" json:"-"`
	PlanID             uuid.UUID  `gorm:"type:uuid;not null;index:idx_delivery_plan_step_activity_timeline,priority:1" json:"-"`
	StepID             uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_step_activity_sequence,priority:1;index:idx_delivery_plan_step_activity_timeline,priority:2" json:"-"`
	AutomationTaskID   uuid.UUID  `gorm:"type:uuid;not null;index" json:"-"`
	RunID              string     `gorm:"type:uuid;not null;uniqueIndex:idx_delivery_plan_step_activity_sequence,priority:2;index" json:"-"`
	WorkerID           string     `gorm:"type:uuid;not null;index" json:"-"`
	AgentKey           string     `gorm:"type:varchar(64);not null;index" json:"-"`
	MachineID          string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	AgentInstanceID    *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	FencingToken       int64      `gorm:"not null" json:"-"`
	Sequence           int64      `gorm:"not null;uniqueIndex:idx_delivery_plan_step_activity_sequence,priority:3;check:delivery_plan_step_activity_sequence_positive,sequence > 0" json:"-"`
	Action             string     `gorm:"type:varchar(24);not null;check:delivery_plan_step_activity_action_allowed,action IN ('inference','tool','file_read','file_change','command','validation','evidence')" json:"-"`
	Phase              string     `gorm:"type:varchar(16);not null;check:delivery_plan_step_activity_phase_allowed,phase IN ('started','completed','failed')" json:"-"`
	InferenceCallID    *uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_delivery_plan_step_activity_inference_call;check:delivery_plan_step_activity_inference_ids,(inference_call_id IS NULL) = (inference_receipt_id IS NULL)" json:"-"`
	InferenceReceiptID *uuid.UUID `gorm:"type:uuid;uniqueIndex:idx_delivery_plan_step_activity_inference_receipt;check:delivery_plan_step_activity_inference_action,inference_receipt_id IS NULL OR (action = 'inference' AND phase IN ('completed','failed'))" json:"-"`
	ToolName           string     `gorm:"type:varchar(64);not null;default:''" json:"-"`
	DurationMS         *int64     `gorm:"check:delivery_plan_step_activity_duration_nonnegative,duration_ms IS NULL OR duration_ms >= 0" json:"-"`
	Summary            string     `gorm:"type:varchar(96);not null" json:"-"`
	DetailsJSON        string     `gorm:"type:jsonb;not null;default:'{}';check:delivery_plan_step_activity_details_object,jsonb_typeof(details_json) = 'object';check:delivery_plan_step_activity_details_size,char_length(details_json::text) <= 4096" json:"-"`
	OccurredAt         time.Time  `gorm:"not null;index:idx_delivery_plan_step_activity_timeline,priority:3" json:"-"`
	CreatedAt          time.Time  `gorm:"not null" json:"-"`

	Plan DeliveryPlan     `gorm:"foreignKey:PlanID;references:ID" json:"-"`
	Step DeliveryPlanStep `gorm:"foreignKey:StepID;references:ID" json:"-"`
}
