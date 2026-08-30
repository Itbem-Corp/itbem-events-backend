// Package deliverypolicy resolves approved delivery policy layers into one
// deterministic, immutable policy for an exact project/repository/change set.
// It performs no I/O and grants no capability by itself.
package deliverypolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/projectvault"
	"events-stocks/internal/releasegate"
)

const SchemaVersion = 1

const maximumOverrideLifetime = 24 * time.Hour

type Level string

const (
	LevelPlatform     Level = "platform"
	LevelOrganization Level = "organization"
	LevelProject      Level = "project"
	LevelRepository   Level = "repository"
	LevelOverride     Level = "override"
)

type DeliveryMode string

const (
	ModeReviewOnly DeliveryMode = "review_only"
	ModeMerge      DeliveryMode = "merge"
	ModeRelease    DeliveryMode = "release"
)

type Context struct {
	OrganizationID string `json:"organization_id"`
	ProjectID      string `json:"project_id"`
	Repository     string `json:"repository"`
	ChangeSetID    string `json:"change_set_id"`
}

// Patch uses pointers so an absent value inherits while an explicit empty
// list can deliberately configure a review-only project with no test runner.
// Safety floors are not fields: no layer can disable them.
type Patch struct {
	Mode                       *DeliveryMode `json:"mode,omitempty"`
	RequiredTestKinds          *[]string     `json:"required_test_kinds,omitempty"`
	AllowedTargetBranches      *[]string     `json:"allowed_target_branches,omitempty"`
	MergeMethod                *string       `json:"merge_method,omitempty"`
	DeploymentWorkflow         *string       `json:"deployment_workflow,omitempty"`
	DeploymentEnvironment      *string       `json:"deployment_environment,omitempty"`
	RequiredSecretReferences   *[]string     `json:"required_secret_references,omitempty"`
	RequiredVariableReferences *[]string     `json:"required_variable_references,omitempty"`
	RequiredHealthChecks       *[]string     `json:"required_health_checks,omitempty"`
	RequiredPostMergeChecks    *[]string     `json:"required_post_merge_checks,omitempty"`
	RecoveryDefault            *string       `json:"recovery_default,omitempty"`
}

// Layer is an immutable policy content revision plus trusted approval
// metadata. Digest seals content/scope; the final resolved digest additionally
// binds the approver and approval time so old approvals cannot be replayed.
type Layer struct {
	SchemaVersion  int        `json:"schema_version"`
	RevisionID     string     `json:"revision_id"`
	Level          Level      `json:"level"`
	OrganizationID string     `json:"organization_id,omitempty"`
	ProjectID      string     `json:"project_id,omitempty"`
	Repository     string     `json:"repository,omitempty"`
	ChangeSetID    string     `json:"change_set_id,omitempty"`
	Patch          Patch      `json:"patch"`
	Reason         string     `json:"reason,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Digest         string     `json:"digest"`
	Approved       bool       `json:"approved"`
	ApprovedBy     string     `json:"approved_by"`
	ApprovedAt     time.Time  `json:"approved_at"`
}

type SafetyFloor struct {
	IndependentReview       bool `json:"independent_review"`
	ExactSHAEvidence        bool `json:"exact_sha_evidence"`
	VaultReconciliation     bool `json:"vault_reconciliation"`
	SecretScan              bool `json:"secret_scan"`
	MaximumHighFindings     int  `json:"maximum_high_findings"`
	MaximumCriticalFindings int  `json:"maximum_critical_findings"`
	Compatibility           bool `json:"compatibility"`
	Migrations              bool `json:"migrations"`
	DependencyOrder         bool `json:"dependency_order"`
	Environment             bool `json:"environment"`
	Recovery                bool `json:"recovery"`
	HumanApproval           bool `json:"human_approval"`
	ForceMergeAllowed       bool `json:"force_merge_allowed"`
}

type Source struct {
	Level      Level     `json:"level"`
	RevisionID string    `json:"revision_id"`
	Digest     string    `json:"digest"`
	ApprovedBy string    `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
}

type ResolvedPolicy struct {
	SchemaVersion              int          `json:"schema_version"`
	Context                    Context      `json:"context"`
	Mode                       DeliveryMode `json:"mode,omitempty"`
	RequiredTestKinds          []string     `json:"required_test_kinds"`
	AllowedTargetBranches      []string     `json:"allowed_target_branches"`
	MergeMethod                string       `json:"merge_method,omitempty"`
	DeploymentWorkflow         string       `json:"deployment_workflow,omitempty"`
	DeploymentEnvironment      string       `json:"deployment_environment,omitempty"`
	RequiredSecretReferences   []string     `json:"required_secret_references"`
	RequiredVariableReferences []string     `json:"required_variable_references"`
	RequiredHealthChecks       []string     `json:"required_health_checks"`
	RequiredPostMergeChecks    []string     `json:"required_post_merge_checks"`
	RecoveryDefault            string       `json:"recovery_default,omitempty"`
	Safety                     SafetyFloor  `json:"safety"`
	Sources                    []Source     `json:"sources"`
	Resolved                   bool         `json:"resolved"`
	Missing                    []string     `json:"missing"`
	Digest                     string       `json:"digest"`
	explicitTests              bool
	explicitBranches           bool
	explicitSecretReferences   bool
	explicitVariableReferences bool
}

var (
	identityPattern             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/+-]{0,127}$`)
	namePattern                 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _./:+-]{0,127}$`)
	workflowPattern             = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]{0,119}\.ya?ml$`)
	environmentReferencePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	digestPattern               = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func safetyFloor() SafetyFloor {
	return SafetyFloor{
		IndependentReview: true, ExactSHAEvidence: true, VaultReconciliation: true, SecretScan: true,
		MaximumHighFindings: 0, MaximumCriticalFindings: 0, Compatibility: true, Migrations: true,
		DependencyOrder: true, Environment: true, Recovery: true, HumanApproval: true, ForceMergeAllowed: false,
	}
}

// LayerDigest seals immutable policy content before a human approval is
// attached. Approval identity/time is bound later by the final policy digest.
func LayerDigest(layer Layer) (string, error) {
	content := struct {
		SchemaVersion  int        `json:"schema_version"`
		RevisionID     string     `json:"revision_id"`
		Level          Level      `json:"level"`
		OrganizationID string     `json:"organization_id,omitempty"`
		ProjectID      string     `json:"project_id,omitempty"`
		Repository     string     `json:"repository,omitempty"`
		ChangeSetID    string     `json:"change_set_id,omitempty"`
		Patch          Patch      `json:"patch"`
		Reason         string     `json:"reason,omitempty"`
		ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	}{
		SchemaVersion: layer.SchemaVersion, RevisionID: strings.TrimSpace(layer.RevisionID), Level: layer.Level,
		OrganizationID: strings.TrimSpace(layer.OrganizationID), ProjectID: strings.TrimSpace(layer.ProjectID),
		Repository: strings.TrimSpace(layer.Repository), ChangeSetID: strings.TrimSpace(layer.ChangeSetID),
		Patch: canonicalPatch(layer.Patch), Reason: strings.TrimSpace(layer.Reason), ExpiresAt: utcTime(layer.ExpiresAt),
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("encode delivery policy layer: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Resolve applies matching approved layers by fixed precedence. Input order
// never changes the result. Missing configuration returns Resolved=false;
// malformed, stale, mismatched, duplicate, or tampered layers return an error.
func Resolve(context Context, layers []Layer, now time.Time) (ResolvedPolicy, error) {
	normalizedContext, err := normalizeContext(context)
	if err != nil || now.IsZero() {
		return ResolvedPolicy{}, fmt.Errorf("delivery policy context is invalid")
	}
	result := ResolvedPolicy{
		SchemaVersion: SchemaVersion, Context: normalizedContext, Safety: safetyFloor(),
		RequiredTestKinds: []string{}, AllowedTargetBranches: []string{}, RequiredHealthChecks: []string{},
		RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{},
		RequiredPostMergeChecks: []string{}, Sources: []Source{}, Missing: []string{},
	}
	ordered := append([]Layer(nil), layers...)
	sort.Slice(ordered, func(left, right int) bool { return precedence(ordered[left].Level) < precedence(ordered[right].Level) })
	seen := map[Level]struct{}{}
	for _, layer := range ordered {
		if _, duplicate := seen[layer.Level]; duplicate {
			return ResolvedPolicy{}, fmt.Errorf("delivery policy contains duplicate %s layer", layer.Level)
		}
		seen[layer.Level] = struct{}{}
		if err := validateLayer(normalizedContext, layer, now.UTC()); err != nil {
			return ResolvedPolicy{}, err
		}
		applyPatch(&result, layer.Patch)
		result.Sources = append(result.Sources, Source{
			Level: layer.Level, RevisionID: strings.TrimSpace(layer.RevisionID), Digest: strings.ToLower(layer.Digest),
			ApprovedBy: strings.TrimSpace(layer.ApprovedBy), ApprovedAt: layer.ApprovedAt.UTC(),
		})
	}
	result.RequiredTestKinds = canonicalNames(result.RequiredTestKinds)
	result.AllowedTargetBranches = canonicalBranches(result.AllowedTargetBranches)
	result.RequiredHealthChecks = canonicalNames(result.RequiredHealthChecks)
	result.RequiredSecretReferences = canonicalEnvironmentReferences(result.RequiredSecretReferences)
	result.RequiredVariableReferences = canonicalEnvironmentReferences(result.RequiredVariableReferences)
	result.RequiredPostMergeChecks = canonicalNames(result.RequiredPostMergeChecks)
	result.Missing = missingConfiguration(result)
	result.Resolved = len(result.Missing) == 0
	result.Digest, err = resolvedDigest(result)
	if err != nil {
		return ResolvedPolicy{}, err
	}
	return result, nil
}

// GatePolicyFor prevents a review-only project from accidentally becoming a
// merge capability and prevents a merge-only project from releasing.
func (policy ResolvedPolicy) GatePolicyFor(action releasegate.Action) releasegate.Policy {
	actionAllowed := (action == releasegate.ActionMerge && (policy.Mode == ModeMerge || policy.Mode == ModeRelease)) ||
		(action == releasegate.ActionRelease && policy.Mode == ModeRelease)
	return releasegate.Policy{
		Resolved: policy.Resolved && actionAllowed && digestPattern.MatchString(policy.Digest),
		Digest:   policy.Digest, RequiredTestKinds: append([]string(nil), policy.RequiredTestKinds...),
	}
}

// AllowsTargetBranch performs an exact, case-sensitive match. Branch policy
// deliberately has no wildcard semantics, so a configured release/v2 cannot
// accidentally authorize release/v2-hotfix or another similarly named ref.
func (policy ResolvedPolicy) AllowsTargetBranch(branch string) bool {
	if !policy.Resolved || branch != strings.TrimSpace(branch) {
		return false
	}
	for _, allowed := range policy.AllowedTargetBranches {
		if branch == allowed {
			return true
		}
	}
	return false
}

func normalizeContext(context Context) (Context, error) {
	context.OrganizationID = strings.TrimSpace(context.OrganizationID)
	context.ProjectID = strings.TrimSpace(context.ProjectID)
	context.ChangeSetID = strings.TrimSpace(context.ChangeSetID)
	if !identityPattern.MatchString(context.OrganizationID) || !identityPattern.MatchString(context.ProjectID) || !identityPattern.MatchString(context.ChangeSetID) {
		return Context{}, fmt.Errorf("policy scope identity is invalid")
	}
	repository, err := projectvault.CanonicalGitHubReference(context.Repository)
	if err != nil {
		return Context{}, err
	}
	context.Repository = repository
	return context, nil
}

func validateLayer(context Context, layer Layer, now time.Time) error {
	if layer.SchemaVersion != SchemaVersion || !identityPattern.MatchString(strings.TrimSpace(layer.RevisionID)) || !layer.Approved ||
		!identityPattern.MatchString(strings.TrimSpace(layer.ApprovedBy)) || layer.ApprovedAt.IsZero() || layer.ApprovedAt.After(now.Add(time.Minute)) {
		return fmt.Errorf("delivery policy %s layer is not an approved current revision", layer.Level)
	}
	expectedDigest, err := LayerDigest(layer)
	if err != nil || !digestPattern.MatchString(strings.ToLower(strings.TrimSpace(layer.Digest))) || !strings.EqualFold(expectedDigest, layer.Digest) {
		return fmt.Errorf("delivery policy %s layer digest does not match", layer.Level)
	}
	if err := validatePatch(layer.Patch); err != nil {
		return fmt.Errorf("delivery policy %s layer: %w", layer.Level, err)
	}
	organizationID, projectID := strings.TrimSpace(layer.OrganizationID), strings.TrimSpace(layer.ProjectID)
	repository, changeSetID := strings.TrimSpace(layer.Repository), strings.TrimSpace(layer.ChangeSetID)
	switch layer.Level {
	case LevelPlatform:
		if organizationID != "" || projectID != "" || repository != "" || changeSetID != "" || layer.ExpiresAt != nil {
			return fmt.Errorf("platform policy must not carry a narrower scope")
		}
	case LevelOrganization:
		if organizationID != context.OrganizationID || projectID != "" || repository != "" || changeSetID != "" || layer.ExpiresAt != nil {
			return fmt.Errorf("organization policy does not match the requested scope")
		}
	case LevelProject:
		if organizationID != context.OrganizationID || projectID != context.ProjectID || repository != "" || changeSetID != "" || layer.ExpiresAt != nil {
			return fmt.Errorf("project policy does not match the requested scope")
		}
	case LevelRepository:
		canonical, canonicalErr := projectvault.CanonicalGitHubReference(repository)
		if canonicalErr != nil || organizationID != context.OrganizationID || projectID != context.ProjectID || canonical != context.Repository || changeSetID != "" || layer.ExpiresAt != nil {
			return fmt.Errorf("repository policy does not match the requested scope")
		}
	case LevelOverride:
		if organizationID != context.OrganizationID || projectID != context.ProjectID || changeSetID != context.ChangeSetID || strings.TrimSpace(layer.Reason) == "" || layer.ExpiresAt == nil || !layer.ExpiresAt.After(now) {
			return fmt.Errorf("approved override is expired or does not match the change set")
		}
		if layer.ExpiresAt.After(layer.ApprovedAt.Add(maximumOverrideLifetime)) {
			return fmt.Errorf("approved override exceeds the maximum lifetime")
		}
		if repository != "" {
			canonical, canonicalErr := projectvault.CanonicalGitHubReference(repository)
			if canonicalErr != nil || canonical != context.Repository {
				return fmt.Errorf("approved override does not match the repository")
			}
		}
	default:
		return fmt.Errorf("delivery policy level is unsupported")
	}
	return nil
}

func validatePatch(patch Patch) error {
	if patch.Mode != nil && *patch.Mode != ModeReviewOnly && *patch.Mode != ModeMerge && *patch.Mode != ModeRelease {
		return fmt.Errorf("delivery mode is invalid")
	}
	if patch.RequiredTestKinds != nil && !validIdentifiers(*patch.RequiredTestKinds, true) {
		return fmt.Errorf("required test kinds are invalid")
	}
	if patch.AllowedTargetBranches != nil && !validBranches(*patch.AllowedTargetBranches) {
		return fmt.Errorf("target branches are invalid")
	}
	if patch.MergeMethod != nil {
		method := strings.ToLower(strings.TrimSpace(*patch.MergeMethod))
		if method != "merge" && method != "squash" && method != "rebase" {
			return fmt.Errorf("merge method is invalid")
		}
	}
	if patch.DeploymentWorkflow != nil && !workflowPattern.MatchString(strings.TrimSpace(*patch.DeploymentWorkflow)) {
		return fmt.Errorf("deployment workflow is invalid")
	}
	if patch.DeploymentEnvironment != nil && !validName(*patch.DeploymentEnvironment) {
		return fmt.Errorf("deployment environment is invalid")
	}
	if patch.RequiredSecretReferences != nil && !validEnvironmentReferences(*patch.RequiredSecretReferences) {
		return fmt.Errorf("required secret references are invalid")
	}
	if patch.RequiredVariableReferences != nil && !validEnvironmentReferences(*patch.RequiredVariableReferences) {
		return fmt.Errorf("required variable references are invalid")
	}
	if patch.RequiredHealthChecks != nil && !validNames(*patch.RequiredHealthChecks, false) {
		return fmt.Errorf("required health checks are invalid")
	}
	if patch.RequiredPostMergeChecks != nil && !validNames(*patch.RequiredPostMergeChecks, false) {
		return fmt.Errorf("required post-merge checks are invalid")
	}
	if patch.RecoveryDefault != nil {
		recovery := strings.ToLower(strings.TrimSpace(*patch.RecoveryDefault))
		if recovery != string(releasegate.RecoveryRollback) && recovery != string(releasegate.RecoveryRollForward) && recovery != string(releasegate.RecoveryExpandContract) && recovery != string(releasegate.RecoveryIrreversible) {
			return fmt.Errorf("recovery default is invalid")
		}
	}
	return nil
}

func applyPatch(policy *ResolvedPolicy, patch Patch) {
	if patch.Mode != nil {
		policy.Mode = *patch.Mode
	}
	if patch.RequiredTestKinds != nil {
		policy.RequiredTestKinds = append([]string(nil), (*patch.RequiredTestKinds)...)
		policy.explicitTests = true
	}
	if patch.AllowedTargetBranches != nil {
		policy.AllowedTargetBranches = append([]string(nil), (*patch.AllowedTargetBranches)...)
		policy.explicitBranches = true
	}
	if patch.MergeMethod != nil {
		policy.MergeMethod = strings.ToLower(strings.TrimSpace(*patch.MergeMethod))
	}
	if patch.DeploymentWorkflow != nil {
		policy.DeploymentWorkflow = strings.TrimSpace(*patch.DeploymentWorkflow)
	}
	if patch.DeploymentEnvironment != nil {
		policy.DeploymentEnvironment = strings.TrimSpace(*patch.DeploymentEnvironment)
	}
	if patch.RequiredSecretReferences != nil {
		policy.RequiredSecretReferences = append([]string(nil), (*patch.RequiredSecretReferences)...)
		policy.explicitSecretReferences = true
	}
	if patch.RequiredVariableReferences != nil {
		policy.RequiredVariableReferences = append([]string(nil), (*patch.RequiredVariableReferences)...)
		policy.explicitVariableReferences = true
	}
	if patch.RequiredHealthChecks != nil {
		policy.RequiredHealthChecks = append([]string(nil), (*patch.RequiredHealthChecks)...)
	}
	if patch.RequiredPostMergeChecks != nil {
		policy.RequiredPostMergeChecks = append([]string(nil), (*patch.RequiredPostMergeChecks)...)
	}
	if patch.RecoveryDefault != nil {
		policy.RecoveryDefault = strings.ToLower(strings.TrimSpace(*patch.RecoveryDefault))
	}
}

func missingConfiguration(policy ResolvedPolicy) []string {
	missing := []string{}
	if policy.Mode == "" {
		missing = append(missing, "mode")
	}
	if !policy.explicitTests || ((policy.Mode == ModeMerge || policy.Mode == ModeRelease) && len(policy.RequiredTestKinds) == 0) {
		missing = append(missing, "required_test_kinds")
	}
	if !policy.explicitBranches || len(policy.AllowedTargetBranches) == 0 {
		missing = append(missing, "allowed_target_branches")
	}
	if (policy.Mode == ModeMerge || policy.Mode == ModeRelease) && policy.MergeMethod == "" {
		missing = append(missing, "merge_method")
	}
	if policy.Mode == ModeRelease {
		if policy.DeploymentWorkflow == "" {
			missing = append(missing, "deployment_workflow")
		}
		if policy.DeploymentEnvironment == "" {
			missing = append(missing, "deployment_environment")
		}
		if !policy.explicitSecretReferences {
			missing = append(missing, "required_secret_references")
		}
		if !policy.explicitVariableReferences {
			missing = append(missing, "required_variable_references")
		}
		if len(policy.RequiredHealthChecks) == 0 {
			missing = append(missing, "required_health_checks")
		}
		if policy.RecoveryDefault == "" {
			missing = append(missing, "recovery_default")
		}
	}
	sort.Strings(missing)
	return missing
}

func resolvedDigest(policy ResolvedPolicy) (string, error) {
	copy := policy
	copy.Digest, copy.explicitTests, copy.explicitBranches = "", false, false
	copy.explicitSecretReferences, copy.explicitVariableReferences = false, false
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode resolved delivery policy: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalPatch(patch Patch) Patch {
	clone := patch
	if patch.Mode != nil {
		value := DeliveryMode(strings.TrimSpace(string(*patch.Mode)))
		clone.Mode = &value
	}
	if patch.MergeMethod != nil {
		value := strings.ToLower(strings.TrimSpace(*patch.MergeMethod))
		clone.MergeMethod = &value
	}
	if patch.DeploymentWorkflow != nil {
		value := strings.TrimSpace(*patch.DeploymentWorkflow)
		clone.DeploymentWorkflow = &value
	}
	if patch.DeploymentEnvironment != nil {
		value := strings.TrimSpace(*patch.DeploymentEnvironment)
		clone.DeploymentEnvironment = &value
	}
	if patch.RequiredSecretReferences != nil {
		values := canonicalEnvironmentReferences(*patch.RequiredSecretReferences)
		clone.RequiredSecretReferences = &values
	}
	if patch.RequiredVariableReferences != nil {
		values := canonicalEnvironmentReferences(*patch.RequiredVariableReferences)
		clone.RequiredVariableReferences = &values
	}
	if patch.RecoveryDefault != nil {
		value := strings.ToLower(strings.TrimSpace(*patch.RecoveryDefault))
		clone.RecoveryDefault = &value
	}
	if patch.RequiredTestKinds != nil {
		values := canonicalNames(*patch.RequiredTestKinds)
		clone.RequiredTestKinds = &values
	}
	if patch.AllowedTargetBranches != nil {
		values := canonicalBranches(*patch.AllowedTargetBranches)
		clone.AllowedTargetBranches = &values
	}
	if patch.RequiredHealthChecks != nil {
		values := canonicalNames(*patch.RequiredHealthChecks)
		clone.RequiredHealthChecks = &values
	}
	if patch.RequiredPostMergeChecks != nil {
		values := canonicalNames(*patch.RequiredPostMergeChecks)
		clone.RequiredPostMergeChecks = &values
	}
	return clone
}

func canonicalNames(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	sort.Slice(result, func(left, right int) bool { return strings.ToLower(result[left]) < strings.ToLower(result[right]) })
	return result
}

func canonicalBranches(values []string) []string {
	return canonicalNames(values)
}

func canonicalEnvironmentReferences(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToUpper(strings.TrimSpace(value))
	}
	sort.Strings(result)
	return result
}

func validEnvironmentReferences(values []string) bool {
	if len(values) > 64 {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		canonical := strings.ToUpper(strings.TrimSpace(value))
		if !environmentReferencePattern.MatchString(canonical) || strings.HasPrefix(canonical, "GITHUB_") {
			return false
		}
		if _, duplicate := seen[canonical]; duplicate {
			return false
		}
		seen[canonical] = struct{}{}
	}
	return true
}

func validNames(values []string, allowEmpty bool) bool {
	if len(values) == 0 {
		return allowEmpty
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if !validName(value) {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validIdentifiers(values []string, allowEmpty bool) bool {
	if len(values) == 0 {
		return allowEmpty
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if !identityPattern.MatchString(value) {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func validName(value string) bool {
	return namePattern.MatchString(strings.TrimSpace(value))
}

func validBranches(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || len(value) > 255 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " \\~^:?*[]\x00\r\n") {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func precedence(level Level) int {
	switch level {
	case LevelPlatform:
		return 0
	case LevelOrganization:
		return 1
	case LevelProject:
		return 2
	case LevelRepository:
		return 3
	case LevelOverride:
		return 4
	default:
		return 99
	}
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
