package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/repositories/awsrepository"
	"events-stocks/services/deliveryplansteps"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxPlanStepActivityDurationMS int64 = 24 * 60 * 60 * 1000

var (
	errPlanStepPatchObjectUnavailable = errors.New("plan-step patch object unavailable")
	getPlanStepPatchObject            = awsrepository.GetS3Object
	planStepPatchSecretPatterns       = []*regexp.Regexp{
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`),
		regexp.MustCompile(`(?i)gh[pousr]_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`(?i)xox[baprs]-[A-Za-z0-9-]{20,}`),
		regexp.MustCompile(`(?i)sk_live_[A-Za-z0-9]{16,}`),
		regexp.MustCompile(`(?i)sk-(?:proj-)?[A-Za-z0-9_-]{24,}`),
		regexp.MustCompile(`sk-ant-api03-[A-Za-z0-9_-]{24,}`),
		regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`npm_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`pypi-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)(?:api[_-]?key|access[_-]?(?:key|token)|client[_-]?secret|password|secret[_-]?(?:access[_-]?)?key)\s*[:=]\s*["']?[A-Za-z0-9/+_=-]{12,}`),
	}
	planStepPatchHunkPattern = regexp.MustCompile(`^@@ -[0-9]+(?:,[0-9]+)? \+[0-9]+(?:,[0-9]+)? @@(?: .*)?$`)
)

var planStepActivityToolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

type planStepActivityRequest struct {
	EventID      string                                  `json:"event_id"`
	TaskID       string                                  `json:"task_id"`
	RunID        string                                  `json:"run_id"`
	WorkerID     string                                  `json:"worker_id"`
	AgentKey     string                                  `json:"agent_key"`
	MachineID    string                                  `json:"machine_id,omitempty"`
	FencingToken string                                  `json:"fencing_token"`
	Sequence     int64                                   `json:"sequence"`
	Action       string                                  `json:"action"`
	Phase        string                                  `json:"phase"`
	CallID       string                                  `json:"call_id,omitempty"`
	ReceiptID    string                                  `json:"receipt_id,omitempty"`
	ToolName     string                                  `json:"tool_name,omitempty"`
	DurationMS   *int64                                  `json:"duration_ms,omitempty"`
	Details      *models.DeliveryPlanStepActivityDetails `json:"details,omitempty"`
}

type planStepActivityResponse struct {
	ID         uuid.UUID `json:"id"`
	Sequence   int64     `json:"sequence"`
	OccurredAt time.Time `json:"occurred_at"`
	Idempotent bool      `json:"idempotent"`
}

// RecordDeliveryPlanStepActivity accepts bounded enums and opaque IDs, plus an
// optional strict terminal-detail projection. Arbitrary summaries, raw paths,
// commands, prompts, tool/provider input, and output are rejected.
func RecordDeliveryPlanStepActivity(c echo.Context) error {
	stepID, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || stepID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan step", "The step identifier is invalid")
	}
	var request planStepActivityRequest
	if err := bindPlanStepRuntimePayload(c, &request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step activity", "The request body is invalid")
	}
	callbackIdentity, ok := bindCallbackProfileIdentity(c, &request.AgentKey, &request.MachineID)
	if !ok {
		return nil
	}
	parsed, err := normalizePlanStepActivityRequest(request)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid plan-step activity", "The event identity or allow-listed activity fields are invalid")
	}
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation unavailable", "Database is unavailable")
	}

	now := time.Now().UTC()
	instanceID := callbackIdentity.InstanceID
	event := models.DeliveryPlanStepActivityEvent{
		ID: parsed.EventID, StepID: stepID, AutomationTaskID: parsed.TaskID,
		RunID: parsed.RunID, WorkerID: parsed.WorkerID, AgentKey: parsed.AgentKey,
		MachineID: parsed.MachineID, AgentInstanceID: &instanceID,
		FencingToken: parsed.Fence, Sequence: parsed.Sequence,
		Action: parsed.Action, Phase: parsed.Phase, ToolName: parsed.ToolName,
		InferenceCallID: parsed.CallID, InferenceReceiptID: parsed.ReceiptID,
		DurationMS: parsed.DurationMS, DetailsJSON: parsed.DetailsJSON, Summary: planStepActivitySummary(parsed.Action, parsed.Phase),
		OccurredAt: now, CreatedAt: now,
	}
	var response planStepActivityResponse
	err = configuration.DB.WithContext(planStepCallbackContext(c, callbackIdentity.InstanceID)).Transaction(func(tx *gorm.DB) error {
		// A durable event can be replayed by its checkpoint after the original
		// request committed but the worker lost the response. Resolve exact,
		// read-only duplicates before checking the current lease: a later run may
		// already have advanced the fence. Collisions with different payloads
		// remain conflicts and never fall through into a write.
		existing, found, err := findPlanStepActivityReplay(tx, stepID, event)
		if err != nil {
			return err
		}
		if found {
			response = planStepActivityResponse{ID: existing.ID, Sequence: existing.Sequence, OccurredAt: existing.OccurredAt, Idempotent: true}
			return nil
		}

		tuple := planStepRuntimeTuple{TaskID: parsed.TaskID, RunID: parsed.RunID, WorkerID: parsed.WorkerID, AgentKey: parsed.AgentKey, MachineID: parsed.MachineID}
		task, err := validatePlanStepRuntimeTask(tx, tuple)
		if err != nil {
			return err
		}
		if err := validatePlanStepActivityInferenceReceipt(tx, *task, callbackIdentity.InstanceID, stepID, parsed); err != nil {
			return err
		}
		if task.Operation != "delivery.implementation" || task.DeliveryWorkItemID == nil {
			return deliveryplansteps.ErrStepLeaseConflict
		}

		var step models.DeliveryPlanStep
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&step, "id = ?", stepID).Error; err != nil {
			return err
		}
		if !planStepActivityLeaseMatches(step, *task, parsed.RunID, parsed.WorkerID, parsed.AgentKey, parsed.MachineID, parsed.Fence, now) {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		if err := validatePlanStepActivityAcceptanceCriteria(step.AcceptanceCriteriaJSON, event); err != nil {
			return deliveryplansteps.ErrStepInputInvalid
		}
		var plan models.DeliveryPlan
		if err := tx.Select("id", "work_item_id", "status", "approved_gate_id").First(&plan, "id = ?", step.PlanID).Error; err != nil {
			return err
		}
		if plan.WorkItemID != *task.DeliveryWorkItemID || plan.Status != "approved" || plan.ApprovedGateID == nil {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		if err := validatePlanStepActivityResourceBindings(tx, plan.WorkItemID, event); err != nil {
			return err
		}
		// This enforces the persisted child-task -> execution -> step binding and
		// approved plan hash for fan-out assignments. Legacy direct claims have
		// no assignment row and are still bound above by their live step tuple.
		if _, err := validateAssignedPlanStepCallback(tx, *task, stepID); err != nil {
			return err
		}
		event.PlanID = plan.ID
		if event.Action == models.DeliveryPlanStepActivityEvidence && event.Phase == models.DeliveryPlanStepActivityCompleted {
			if err := validateAppliedPlanStepDependencyPatchManifest(tx, step, *task, automationagent.AgentIdentity{
				WorkerID: parsed.WorkerID, AgentKey: parsed.AgentKey, MachineID: parsed.MachineID,
			}, callbackIdentity.InstanceID, parsed.Fence, event); err != nil {
				return err
			}
		}
		if err := validateAndPersistPlanStepPatchArtifacts(tx, c.Request().Context(), c.Get("config"), step, *task, callbackIdentity, parsed, event); err != nil {
			return err
		}

		// DoNothing makes concurrent event_id/sequence retries observable without
		// aborting the PostgreSQL transaction. A duplicate is acknowledged only
		// when its persisted, allow-listed semantic payload matches exactly.
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			response = planStepActivityResponse{ID: event.ID, Sequence: event.Sequence, OccurredAt: event.OccurredAt, Idempotent: false}
			return nil
		}
		existing, found, err = findPlanStepActivityReplay(tx, stepID, event)
		if err != nil {
			return err
		}
		if !found {
			return deliveryplansteps.ErrStepLeaseConflict
		}
		response = planStepActivityResponse{ID: existing.ID, Sequence: existing.Sequence, OccurredAt: existing.OccurredAt, Idempotent: true}
		return nil
	})
	if err != nil {
		if errors.Is(err, errPlanStepPatchObjectUnavailable) {
			return utils.Error(c, http.StatusServiceUnavailable, "Plan-step activity rejected", "The private patch artifact could not be verified")
		}
		return planStepRuntimeError(c, "Plan-step activity rejected", err)
	}
	return utils.Success(c, http.StatusOK, "Plan-step activity recorded", response)
}

// validatePlanStepActivityAcceptanceCriteria binds the worker's redacted
// acceptance attestation to the exact UTF-8 criterion text frozen on this
// step. It verifies metadata only; it is not cryptographic proof that checks
// were actually executed by the worker.
func validatePlanStepActivityAcceptanceCriteria(criteriaJSON string, event models.DeliveryPlanStepActivityEvent) error {
	details, err := parsePlanStepActivityDetailsJSON(event.DetailsJSON, event.Action, event.Phase)
	if err != nil {
		return err
	}
	if details == nil {
		return nil
	}
	hasPatchArtifacts := len(details.PatchArtifacts) > 0
	if hasPatchArtifacts {
		manifestSHA256, manifestErr := models.DeliveryPlanStepPatchArtifactManifestSHA256(details.PatchArtifacts)
		if manifestErr != nil || event.Action != models.DeliveryPlanStepActivityEvidence || event.Phase != models.DeliveryPlanStepActivityCompleted || details.ReviewDiffSHA256 != manifestSHA256 {
			return deliveryplansteps.ErrStepInputInvalid
		}
	} else if len(details.AcceptanceChecks) == 0 && details.ReviewDiffSHA256 == "" {
		return nil
	}
	if hasPatchArtifacts && len(details.AcceptanceChecks) == 0 {
		return nil
	}
	if event.Action != models.DeliveryPlanStepActivityEvidence || event.Phase != models.DeliveryPlanStepActivityCompleted || len(details.AcceptanceChecks) == 0 {
		return deliveryplansteps.ErrStepInputInvalid
	}
	var criteria []string
	if err := json.Unmarshal([]byte(criteriaJSON), &criteria); err != nil || len(criteria) == 0 || len(criteria) != len(details.AcceptanceChecks) {
		return deliveryplansteps.ErrStepInputInvalid
	}
	expected := make(map[string]struct{}, len(criteria))
	for _, criterion := range criteria {
		if criterion == "" {
			return deliveryplansteps.ErrStepInputInvalid
		}
		digest := sha256.Sum256([]byte(criterion))
		key := hex.EncodeToString(digest[:])
		if _, duplicate := expected[key]; duplicate {
			return deliveryplansteps.ErrStepInputInvalid
		}
		expected[key] = struct{}{}
	}
	for _, check := range details.AcceptanceChecks {
		if !check.Passed {
			return deliveryplansteps.ErrStepInputInvalid
		}
		if _, matched := expected[check.CriterionSHA256]; !matched {
			return deliveryplansteps.ErrStepInputInvalid
		}
		delete(expected, check.CriterionSHA256)
	}
	if len(expected) != 0 {
		return deliveryplansteps.ErrStepInputInvalid
	}
	return nil
}

func validateAndPersistPlanStepPatchArtifacts(tx *gorm.DB, ctx context.Context, rawConfig any, step models.DeliveryPlanStep, task models.AutomationTask, identity authenticatedAgentCallback, parsed normalizedPlanStepActivity, event models.DeliveryPlanStepActivityEvent) error {
	details, err := parsePlanStepActivityDetailsJSON(event.DetailsJSON, event.Action, event.Phase)
	if err != nil || details == nil || len(details.PatchArtifacts) == 0 {
		return err
	}
	cfg, ok := rawConfig.(*models.Config)
	if !ok || cfg == nil || strings.TrimSpace(cfg.AutomationOutputBucket) == "" || strings.TrimSpace(cfg.AutomationOutputBucket) != cfg.AutomationOutputBucket {
		return errPlanStepPatchObjectUnavailable
	}
	bucket := cfg.AutomationOutputBucket

	var snapshots []models.DeliveryContextSnapshot
	if err := tx.Select("reference").Where("work_item_id = ? AND LOWER(kind) = ?", *task.DeliveryWorkItemID, "repository").Find(&snapshots).Error; err != nil {
		return err
	}
	allowedRepositories := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		if models.IsSafeDeliveryPlanStepPatchRepositoryReference(snapshot.Reference) {
			allowedRepositories[snapshot.Reference] = struct{}{}
		}
	}

	for _, reference := range details.PatchArtifacts {
		if _, found := allowedRepositories[reference.RepositoryRef]; !found {
			return deliveryplansteps.ErrStepInputInvalid
		}
		objectKey := planStepPatchObjectKey(task.ID, parsed.RunID, step.ID, reference.SHA256)
		object, err := getPlanStepPatchObject(ctx, objectKey, bucket)
		if err != nil {
			return errPlanStepPatchObjectUnavailable
		}
		patch, readErr := io.ReadAll(io.LimitReader(object, int64(models.MaxDeliveryPlanStepPatchArtifactBytes)+1))
		closeErr := object.Close()
		if readErr != nil || closeErr != nil {
			return errPlanStepPatchObjectUnavailable
		}
		if int64(len(patch)) != reference.SizeBytes || len(patch) > models.MaxDeliveryPlanStepPatchArtifactBytes {
			return deliveryplansteps.ErrStepInputInvalid
		}
		digest := sha256.Sum256(patch)
		if hex.EncodeToString(digest[:]) != reference.SHA256 || validateUnifiedGitPatch(patch) != nil || containsHighConfidencePatchSecret(patch) {
			return deliveryplansteps.ErrStepInputInvalid
		}

		artifact := models.DeliveryPlanStepPatchArtifact{
			PlanID: event.PlanID, AutomationTaskID: task.ID, RunID: parsed.RunID, StepID: step.ID,
			WorkerID: parsed.WorkerID, AgentKey: parsed.AgentKey, MachineID: parsed.MachineID,
			AgentInstanceID: &identity.InstanceID, FencingToken: parsed.Fence,
			RepositoryRef: reference.RepositoryRef, BaseSHA: reference.BaseSHA, Bucket: bucket, ObjectKey: objectKey,
			SHA256: reference.SHA256, SizeBytes: reference.SizeBytes, CreatedAt: event.CreatedAt,
		}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "run_id"}, {Name: "step_id"}, {Name: "repository_ref"}},
			DoNothing: true,
		}).Create(&artifact)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			continue
		}
		var existing models.DeliveryPlanStepPatchArtifact
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&existing, "run_id = ? AND step_id = ? AND repository_ref = ?", artifact.RunID, artifact.StepID, artifact.RepositoryRef).Error; err != nil {
			return err
		}
		if !sameDeliveryPlanStepPatchArtifact(existing, artifact) {
			return deliveryplansteps.ErrStepLeaseConflict
		}
	}
	return nil
}

func planStepPatchObjectKey(taskID uuid.UUID, runID string, stepID uuid.UUID, digest string) string {
	return "automation/" + taskID.String() + "/runs/" + runID + "/steps/" + stepID.String() + "/patches/" + digest + ".patch"
}

func sameDeliveryPlanStepPatchArtifact(left, right models.DeliveryPlanStepPatchArtifact) bool {
	return left.PlanID == right.PlanID && left.AutomationTaskID == right.AutomationTaskID && left.RunID == right.RunID && left.StepID == right.StepID &&
		left.WorkerID == right.WorkerID && left.AgentKey == right.AgentKey && left.MachineID == right.MachineID &&
		equalUUIDPointer(left.AgentInstanceID, right.AgentInstanceID) && left.FencingToken == right.FencingToken &&
		left.RepositoryRef == right.RepositoryRef && left.BaseSHA == right.BaseSHA && left.Bucket == right.Bucket && left.ObjectKey == right.ObjectKey &&
		left.SHA256 == right.SHA256 && left.SizeBytes == right.SizeBytes
}

func equalUUIDPointer(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// validateUnifiedGitPatch accepts textual, unquoted Git unified diffs only.
// Rejecting quoted, renamed, copied, binary, or malformed paths avoids trying
// to reinterpret C-escaped Git path syntax at this trust boundary.
func validateUnifiedGitPatch(patch []byte) error {
	if len(patch) == 0 || !utf8.Valid(patch) || bytes.IndexByte(patch, 0) >= 0 {
		return errors.New("patch is not valid UTF-8 text")
	}
	text := strings.ReplaceAll(string(patch), "\r\n", "\n")
	if strings.ContainsRune(text, '\r') {
		return errors.New("patch contains invalid line endings")
	}
	lines := strings.Split(text, "\n")
	sections := 0
	oldPath, newPath, expectedOldPath, expectedNewPath := "", "", "", ""
	insideHunk, sectionHasHunk := false, false
	finishSection := func() error {
		if oldPath == "" || newPath == "" || (oldPath == "/dev/null" && newPath == "/dev/null") ||
			(oldPath != "/dev/null" && oldPath != expectedOldPath) || (newPath != "/dev/null" && newPath != expectedNewPath) || !sectionHasHunk {
			return errors.New("patch section is missing file headers")
		}
		return nil
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "GIT binary patch") || strings.HasPrefix(line, "Binary files ") {
			return errors.New("binary patches are not accepted")
		}
		if strings.HasPrefix(line, "diff --git ") {
			if sections > 0 {
				if err := finishSection(); err != nil {
					return err
				}
			}
			fields := strings.Fields(line)
			if len(fields) != 4 || fields[0] != "diff" || fields[1] != "--git" || strings.ContainsAny(line, `"'`) {
				return errors.New("patch has an invalid Git file header")
			}
			expectedOldPath, expectedNewPath = strings.TrimPrefix(fields[2], "a/"), strings.TrimPrefix(fields[3], "b/")
			if expectedOldPath == fields[2] || expectedNewPath == fields[3] || !models.IsSafeDeliveryPlanStepPatchPath(expectedOldPath) || !models.IsSafeDeliveryPlanStepPatchPath(expectedNewPath) {
				return errors.New("patch file path is unsafe")
			}
			sections++
			oldPath, newPath = "", ""
			insideHunk, sectionHasHunk = false, false
			continue
		}
		if sections == 0 {
			if strings.TrimSpace(line) != "" {
				return errors.New("patch must begin with a Git file header")
			}
			continue
		}
		if strings.HasPrefix(line, "@@") {
			if !planStepPatchHunkPattern.MatchString(line) {
				return errors.New("patch contains an invalid unified-diff hunk header")
			}
			insideHunk = true
			sectionHasHunk = true
			continue
		}
		if insideHunk {
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			if oldPath != "" {
				return errors.New("patch has duplicate old-file headers")
			}
			oldPath = strings.TrimPrefix(line, "--- ")
			if oldPath != "/dev/null" {
				rawPath := oldPath
				oldPath = strings.TrimPrefix(oldPath, "a/")
				if !strings.HasPrefix(rawPath, "a/") || !models.IsSafeDeliveryPlanStepPatchPath(oldPath) {
					return errors.New("patch old-file path is unsafe")
				}
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			if oldPath == "" || newPath != "" {
				return errors.New("patch has invalid new-file header ordering")
			}
			newPath = strings.TrimPrefix(line, "+++ ")
			if newPath != "/dev/null" {
				newPath = strings.TrimPrefix(newPath, "b/")
				if !strings.HasPrefix(strings.TrimPrefix(line, "+++ "), "b/") || !models.IsSafeDeliveryPlanStepPatchPath(newPath) {
					return errors.New("patch new-file path is unsafe")
				}
			}
			continue
		}
	}
	if sections == 0 {
		return errors.New("patch contains no Git file sections")
	}
	if err := finishSection(); err != nil {
		return err
	}
	return nil
}

func containsHighConfidencePatchSecret(patch []byte) bool {
	for _, pattern := range planStepPatchSecretPatterns {
		if pattern.Match(patch) {
			return true
		}
	}
	return false
}

func validatePlanStepActivityResourceBindings(tx *gorm.DB, workItemID uuid.UUID, event models.DeliveryPlanStepActivityEvent) error {
	details, err := parsePlanStepActivityDetailsJSON(event.DetailsJSON, event.Action, event.Phase)
	if err != nil || details == nil || len(details.ResourceReferences) == 0 {
		return err
	}
	var snapshots []models.DeliveryContextSnapshot
	if err := tx.Select("kind", "reference").Where("work_item_id = ? AND LOWER(kind) = ?", workItemID, "repository").Find(&snapshots).Error; err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		root := strings.TrimSpace(snapshot.Reference)
		if strings.HasPrefix(root, "workspace://") && !strings.Contains(strings.TrimPrefix(root, "workspace://"), "/") {
			allowed[root] = struct{}{}
		}
	}
	for _, reference := range details.ResourceReferences {
		remainder := strings.TrimPrefix(reference, "workspace://")
		workspaceID := strings.SplitN(remainder, "/", 2)[0]
		if _, found := allowed["workspace://"+workspaceID]; !found {
			return deliveryplansteps.ErrStepLeaseConflict
		}
	}
	return nil
}

// findPlanStepActivityReplay checks both durable idempotency keys. A replay is
// accepted only if every payload-derived field, including event ID, fencing
// token and step/plan binding, matches the stored event. The database-generated
// occurrence timestamps are intentionally not compared.
func findPlanStepActivityReplay(tx *gorm.DB, stepID uuid.UUID, incoming models.DeliveryPlanStepActivityEvent) (*models.DeliveryPlanStepActivityEvent, bool, error) {
	if tx == nil || stepID == uuid.Nil || incoming.ID == uuid.Nil {
		return nil, false, deliveryplansteps.ErrStepInputInvalid
	}
	var existing models.DeliveryPlanStepActivityEvent
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&existing, "id = ?", incoming.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("step_id = ? AND run_id = ? AND sequence = ?", stepID, incoming.RunID, incoming.Sequence).First(&existing).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if existing.StepID != stepID {
		return nil, false, deliveryplansteps.ErrStepLeaseConflict
	}
	var step models.DeliveryPlanStep
	if err := tx.Select("id", "plan_id").Clauses(clause.Locking{Strength: "SHARE"}).First(&step, "id = ?", stepID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, deliveryplansteps.ErrStepLeaseConflict
		}
		return nil, false, err
	}
	incoming.PlanID = step.PlanID
	if !samePlanStepActivity(existing, incoming) {
		return nil, false, deliveryplansteps.ErrStepLeaseConflict
	}
	return &existing, true, nil
}

type normalizedPlanStepActivity struct {
	EventID     uuid.UUID
	TaskID      uuid.UUID
	RunID       string
	WorkerID    string
	AgentKey    string
	MachineID   string
	Fence       int64
	Sequence    int64
	Action      string
	Phase       string
	CallID      *uuid.UUID
	ReceiptID   *uuid.UUID
	ToolName    string
	DurationMS  *int64
	DetailsJSON string
}

func normalizePlanStepActivityRequest(request planStepActivityRequest) (normalizedPlanStepActivity, error) {
	eventID, err := uuid.FromString(strings.TrimSpace(request.EventID))
	if err != nil || eventID == uuid.Nil {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	taskID, runID, identity, _, tupleErr := parsePlanStepRuntimeTuple(request.TaskID, request.RunID, request.WorkerID, request.AgentKey, request.MachineID, 0)
	fence, fenceErr := parsePlanStepFence(request.FencingToken)
	if tupleErr != nil || fenceErr != nil || request.Sequence < 1 {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	phase := strings.ToLower(strings.TrimSpace(request.Phase))
	if !validPlanStepActivityAction(action) || !validPlanStepActivityPhase(phase) {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	callIDRaw, receiptIDRaw := strings.TrimSpace(request.CallID), strings.TrimSpace(request.ReceiptID)
	if callIDRaw != request.CallID || receiptIDRaw != request.ReceiptID || (callIDRaw == "") != (receiptIDRaw == "") {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	var callID, receiptID *uuid.UUID
	if callIDRaw != "" {
		parsedCallID, callErr := uuid.FromString(callIDRaw)
		parsedReceiptID, receiptErr := uuid.FromString(receiptIDRaw)
		if callErr != nil || receiptErr != nil || parsedCallID == uuid.Nil || parsedReceiptID == uuid.Nil || parsedCallID.String() != callIDRaw || parsedReceiptID.String() != receiptIDRaw || action != models.DeliveryPlanStepActivityInference || phase == models.DeliveryPlanStepActivityStarted {
			return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
		}
		callID, receiptID = &parsedCallID, &parsedReceiptID
	}
	if action == models.DeliveryPlanStepActivityInference && phase == models.DeliveryPlanStepActivityCompleted && callID == nil {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	toolName := strings.TrimSpace(request.ToolName)
	if (action == models.DeliveryPlanStepActivityTool && toolName != "" && !planStepActivityToolNamePattern.MatchString(toolName)) || (action != models.DeliveryPlanStepActivityTool && toolName != "") {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	if request.DurationMS != nil && (*request.DurationMS < 0 || *request.DurationMS > maxPlanStepActivityDurationMS || phase == models.DeliveryPlanStepActivityStarted) {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	if err := models.ValidateDeliveryPlanStepActivityDetails(action, phase, request.Details); err != nil {
		return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
	}
	detailsJSON := "{}"
	if request.Details != nil {
		encoded, marshalErr := json.Marshal(request.Details)
		if marshalErr != nil || len(encoded) > models.MaxDeliveryPlanStepActivityDetailsBytes {
			return normalizedPlanStepActivity{}, deliveryplansteps.ErrStepInputInvalid
		}
		detailsJSON = string(encoded)
	}
	return normalizedPlanStepActivity{
		EventID: eventID, TaskID: taskID, RunID: runID, WorkerID: identity.WorkerID,
		AgentKey: identity.AgentKey, MachineID: identity.MachineID, Fence: fence,
		Sequence: request.Sequence, Action: action, Phase: phase, CallID: callID, ReceiptID: receiptID, ToolName: toolName, DurationMS: request.DurationMS, DetailsJSON: detailsJSON,
	}, nil
}

// validatePlanStepActivityInferenceReceipt proves that the worker's opaque
// identifiers resolve to a finalized canonical gateway receipt for this exact
// task/run/worker tuple. The authenticated instance is checked against the
// task's active assignment because receipts currently do not persist an
// AgentInstanceID column.
func validatePlanStepActivityInferenceReceipt(tx *gorm.DB, task models.AutomationTask, callbackInstanceID, stepID uuid.UUID, parsed normalizedPlanStepActivity) error {
	if parsed.CallID == nil && parsed.ReceiptID == nil {
		return nil
	}
	if tx == nil || parsed.CallID == nil || parsed.ReceiptID == nil || callbackInstanceID == uuid.Nil || stepID == uuid.Nil || task.AgentInstanceID == nil || *task.AgentInstanceID != callbackInstanceID {
		return deliveryplansteps.ErrStepLeaseConflict
	}
	var receipt models.AutomationInferenceReceipt
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where(
		"id = ? AND automation_task_id = ? AND run_id = ? AND call_id = ? AND worker_id = ? AND agent_key = ? AND machine_id = ? AND status IN ?",
		*parsed.ReceiptID, task.ID, parsed.RunID, *parsed.CallID, parsed.WorkerID, parsed.AgentKey, parsed.MachineID, []string{"accepted", "rejected"},
	).First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return deliveryplansteps.ErrStepLeaseConflict
	}
	if err != nil {
		return err
	}
	if (parsed.Phase == models.DeliveryPlanStepActivityCompleted && receipt.Status != "accepted") ||
		receipt.ID != *parsed.ReceiptID || receipt.CallID != *parsed.CallID || receipt.AutomationTaskID != task.ID || receipt.RunID != parsed.RunID || receipt.WorkerID != parsed.WorkerID || receipt.AgentKey != parsed.AgentKey || receipt.MachineID != parsed.MachineID ||
		receipt.PlanStepID == nil || *receipt.PlanStepID != stepID ||
		receipt.Provider == "" || receipt.Model == "" || receipt.InputTokens < 0 || receipt.OutputTokens < 0 || receipt.CachedInputTokens < 0 || receipt.CacheWriteTokens < 0 || receipt.ReasoningTokens < 0 || receipt.TotalTokens < 0 || receipt.TotalCostMicros < 0 {
		return deliveryplansteps.ErrStepLeaseConflict
	}
	return nil
}

func validPlanStepActivityAction(action string) bool {
	switch action {
	case models.DeliveryPlanStepActivityInference, models.DeliveryPlanStepActivityTool,
		models.DeliveryPlanStepActivityFileRead, models.DeliveryPlanStepActivityFileChange,
		models.DeliveryPlanStepActivityCommand, models.DeliveryPlanStepActivityValidation,
		models.DeliveryPlanStepActivityEvidence:
		return true
	default:
		return false
	}
}

func validPlanStepActivityPhase(phase string) bool {
	return phase == models.DeliveryPlanStepActivityStarted || phase == models.DeliveryPlanStepActivityCompleted || phase == models.DeliveryPlanStepActivityFailed
}

func planStepActivitySummary(action, phase string) string {
	labels := map[string]string{
		models.DeliveryPlanStepActivityInference:  "Inference",
		models.DeliveryPlanStepActivityTool:       "Tool",
		models.DeliveryPlanStepActivityFileRead:   "File read",
		models.DeliveryPlanStepActivityFileChange: "File change",
		models.DeliveryPlanStepActivityCommand:    "Command",
		models.DeliveryPlanStepActivityValidation: "Validation",
		models.DeliveryPlanStepActivityEvidence:   "Evidence",
	}
	label, ok := labels[action]
	if !ok || !validPlanStepActivityPhase(phase) {
		return "Step activity recorded"
	}
	return label + " " + phase
}

func samePlanStepActivity(existing, incoming models.DeliveryPlanStepActivityEvent) bool {
	if existing.ID != incoming.ID {
		return false
	}
	return existing.PlanID == incoming.PlanID && existing.StepID == incoming.StepID &&
		existing.AutomationTaskID == incoming.AutomationTaskID && existing.RunID == incoming.RunID &&
		existing.WorkerID == incoming.WorkerID && existing.AgentKey == incoming.AgentKey && existing.MachineID == incoming.MachineID &&
		equalUUIDPointer(existing.InferenceCallID, incoming.InferenceCallID) && equalUUIDPointer(existing.InferenceReceiptID, incoming.InferenceReceiptID) &&
		existing.FencingToken == incoming.FencingToken && existing.Sequence == incoming.Sequence &&
		existing.Action == incoming.Action && existing.Phase == incoming.Phase && existing.ToolName == incoming.ToolName &&
		equalDurationMS(existing.DurationMS, incoming.DurationMS) && existing.Summary == incoming.Summary &&
		equalPlanStepActivityDetailsJSON(existing.DetailsJSON, incoming.DetailsJSON, existing.Action, existing.Phase)
}

func equalPlanStepActivityDetailsJSON(left, right, action, phase string) bool {
	leftDetails, leftErr := parsePlanStepActivityDetailsJSON(left, action, phase)
	rightDetails, rightErr := parsePlanStepActivityDetailsJSON(right, action, phase)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftDetails)
	rightJSON, rightErr := json.Marshal(rightDetails)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func parsePlanStepActivityDetailsJSON(raw, action, phase string) (*models.DeliveryPlanStepActivityDetails, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" {
		return nil, nil
	}
	if trimmed == "null" || len(trimmed) > models.MaxDeliveryPlanStepActivityDetailsBytes {
		return nil, errors.New("activity details cannot be null")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var details models.DeliveryPlanStepActivityDetails
	if err := decoder.Decode(&details); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("activity details contain multiple JSON values")
		}
		return nil, err
	}
	if err := models.ValidateDeliveryPlanStepActivityDetails(action, phase, &details); err != nil {
		return nil, err
	}
	return &details, nil
}

func equalDurationMS(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func planStepActivityLeaseMatches(step models.DeliveryPlanStep, task models.AutomationTask, runID, workerID, agentKey, machineID string, fence int64, now time.Time) bool {
	return step.PlanID != uuid.Nil && step.Status == models.DeliveryPlanStepRunning &&
		step.AutomationTaskID != nil && *step.AutomationTaskID == task.ID && task.ID != uuid.Nil &&
		step.RunID == runID && task.RunID == runID && step.WorkerID == workerID && task.WorkerID == workerID &&
		step.AgentKey == agentKey && task.AgentKey == agentKey && step.MachineID == machineID && task.MachineID == machineID &&
		step.LeaseFence == fence && fence > 0 && step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(now) &&
		task.LeaseExpiresAt != nil && task.LeaseExpiresAt.After(now)
}
