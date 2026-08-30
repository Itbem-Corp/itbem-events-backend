package delivery

import (
	"net/http"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/deliverypolicystore"
	"events-stocks/internal/projectvault"
	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const effectivePolicyRevisionLimit = 500

type effectivePolicySource struct {
	Level      deliverypolicy.Level `json:"level"`
	RevisionID string               `json:"revision_id"`
	Digest     string               `json:"digest"`
	ApprovedAt time.Time            `json:"approved_at"`
}

type effectivePolicyProjection struct {
	SchemaVersion              int                         `json:"schema_version"`
	Mode                       deliverypolicy.DeliveryMode `json:"mode,omitempty"`
	RequiredTestKinds          []string                    `json:"required_test_kinds"`
	AllowedTargetBranches      []string                    `json:"allowed_target_branches"`
	MergeMethod                string                      `json:"merge_method,omitempty"`
	DeploymentWorkflow         string                      `json:"deployment_workflow,omitempty"`
	DeploymentEnvironment      string                      `json:"deployment_environment,omitempty"`
	RequiredSecretReferences   []string                    `json:"required_secret_references"`
	RequiredVariableReferences []string                    `json:"required_variable_references"`
	RequiredHealthChecks       []string                    `json:"required_health_checks"`
	RequiredPostMergeChecks    []string                    `json:"required_post_merge_checks"`
	RecoveryDefault            string                      `json:"recovery_default,omitempty"`
	Safety                     deliverypolicy.SafetyFloor  `json:"safety"`
	Sources                    []effectivePolicySource     `json:"sources"`
	Resolved                   bool                        `json:"resolved"`
	Missing                    []string                    `json:"missing"`
	Digest                     string                      `json:"digest"`
}

type effectivePolicyVaultEvidence struct {
	RevisionID    uuid.UUID `json:"revision_id"`
	Version       int64     `json:"version"`
	RepositorySHA string    `json:"repository_sha"`
	ContentSHA256 string    `json:"content_sha256"`
}

type effectivePolicySnapshot struct {
	SchemaVersion       int                          `json:"schema_version"`
	ProjectID           uuid.UUID                    `json:"project_id"`
	Repository          string                       `json:"repository"`
	ChangeSetID         string                       `json:"change_set_id,omitempty"`
	OverridesConsidered bool                         `json:"overrides_considered"`
	EvaluatedAt         time.Time                    `json:"evaluated_at"`
	Vault               effectivePolicyVaultEvidence `json:"vault"`
	Policy              effectivePolicyProjection    `json:"policy"`
}

// GetEffectiveProjectPolicy is a bounded, read-only projection. It only
// evaluates repositories already published into this project's immutable
// Vault and never exposes raw policy actor identities.
func GetEffectiveProjectPolicy(c echo.Context) error {
	projectID, err := id(c, "project")
	if err != nil {
		return err
	}
	if _, err := projectActor(c, projectID, deliveryView); err != nil {
		return err
	}
	var project models.DeliveryProject
	if err := configuration.DB.Select("id", "client_id").First(&project, projectID).Error; err != nil {
		return lookup(c, "Delivery project", err)
	}
	repository, err := projectvault.CanonicalGitHubReference(c.QueryParam("repository"))
	if err != nil {
		return badRequest(c, "Invalid delivery policy context", "repository must identify one approved GitHub repository")
	}
	var vault models.DeliveryProjectVaultRevision
	if err := configuration.DB.Where("project_id = ? AND repository_reference = ?", projectID, repository).Order("version DESC").First(&vault).Error; err != nil {
		return lookup(c, "Project Vault repository", err)
	}

	changeSetID := strings.TrimSpace(c.QueryParam("change_set_id"))
	overridesConsidered := changeSetID != ""
	contextChangeSetID := changeSetID
	if !overridesConsidered {
		contextChangeSetID = "policy-preview"
	}
	now := time.Now().UTC()
	context := deliverypolicy.Context{
		OrganizationID: project.ClientID.String(), ProjectID: projectID.String(), Repository: repository, ChangeSetID: contextChangeSetID,
	}
	contextCheck, err := deliverypolicy.Resolve(context, nil, now)
	if err != nil {
		return badRequest(c, "Invalid delivery policy context", "change_set_id is invalid")
	}
	context = contextCheck.Context

	scopeSQL := `(level = 'platform') OR
		(level = 'organization' AND organization_id = ?) OR
		(level = 'project' AND organization_id = ? AND project_id = ?) OR
		(level = 'repository' AND organization_id = ? AND project_id = ? AND repository_reference = ?)`
	args := []any{context.OrganizationID, context.OrganizationID, projectID, context.OrganizationID, projectID, repository}
	if overridesConsidered {
		scopeSQL += ` OR (level = 'override' AND organization_id = ? AND project_id = ? AND change_set_id = ? AND (repository_reference = '' OR repository_reference = ?))`
		args = append(args, context.OrganizationID, projectID, context.ChangeSetID, repository)
	}
	var revisions []models.DeliveryPolicyRevision
	if err := configuration.DB.Where(scopeSQL, args...).Order("created_at DESC, id DESC").Limit(effectivePolicyRevisionLimit + 1).Find(&revisions).Error; err != nil {
		return deliveryPolicyReadError(c)
	}
	if len(revisions) > effectivePolicyRevisionLimit {
		return conflict(c, "Delivery policy history is too large", "Policy evaluation stopped safely; compact the read model before retrying")
	}

	decisions := []models.DeliveryPolicyDecision{}
	if len(revisions) > 0 {
		revisionIDs := make([]uuid.UUID, 0, len(revisions))
		for _, revision := range revisions {
			revisionIDs = append(revisionIDs, revision.ID)
		}
		if err := configuration.DB.Select("DISTINCT ON (policy_revision_id) delivery_policy_decisions.*").
			Where("policy_revision_id IN ?", revisionIDs).
			Order("policy_revision_id, occurred_at DESC, id DESC").Find(&decisions).Error; err != nil {
			return deliveryPolicyReadError(c)
		}
	}
	policy, err := deliverypolicystore.ResolveEffective(context, revisions, decisions, now)
	if err != nil {
		return conflict(c, "Delivery policy evidence failed integrity checks", "Policy evaluation stopped safely; no merge or release authority was granted")
	}
	snapshot := buildEffectivePolicySnapshot(projectID, repository, changeSetID, overridesConsidered, vault, policy, now)
	return success(c, "Effective delivery policy", snapshot)
}

func buildEffectivePolicySnapshot(projectID uuid.UUID, repository, changeSetID string, overridesConsidered bool, vault models.DeliveryProjectVaultRevision, policy deliverypolicy.ResolvedPolicy, evaluatedAt time.Time) effectivePolicySnapshot {
	sources := make([]effectivePolicySource, 0, len(policy.Sources))
	for _, source := range policy.Sources {
		sources = append(sources, effectivePolicySource{Level: source.Level, RevisionID: source.RevisionID, Digest: source.Digest, ApprovedAt: source.ApprovedAt})
	}
	projection := effectivePolicyProjection{
		SchemaVersion: policy.SchemaVersion, Mode: policy.Mode,
		RequiredTestKinds: append([]string{}, policy.RequiredTestKinds...), AllowedTargetBranches: append([]string{}, policy.AllowedTargetBranches...),
		MergeMethod: policy.MergeMethod, DeploymentWorkflow: policy.DeploymentWorkflow, DeploymentEnvironment: policy.DeploymentEnvironment,
		RequiredSecretReferences: append([]string{}, policy.RequiredSecretReferences...), RequiredVariableReferences: append([]string{}, policy.RequiredVariableReferences...),
		RequiredHealthChecks: append([]string{}, policy.RequiredHealthChecks...), RequiredPostMergeChecks: append([]string{}, policy.RequiredPostMergeChecks...),
		RecoveryDefault: policy.RecoveryDefault, Safety: policy.Safety, Sources: sources,
		Resolved: policy.Resolved, Missing: append([]string{}, policy.Missing...), Digest: policy.Digest,
	}
	return effectivePolicySnapshot{
		SchemaVersion: 1, ProjectID: projectID, Repository: repository, ChangeSetID: changeSetID,
		OverridesConsidered: overridesConsidered, EvaluatedAt: evaluatedAt.UTC(),
		Vault:  effectivePolicyVaultEvidence{RevisionID: vault.ID, Version: vault.Version, RepositorySHA: vault.Revision, ContentSHA256: vault.ContentSHA256},
		Policy: projection,
	}
}

func deliveryPolicyReadError(c echo.Context) error {
	return utils.Error(c, http.StatusInternalServerError, "Delivery policy unavailable", "Could not load delivery policy evidence")
}
