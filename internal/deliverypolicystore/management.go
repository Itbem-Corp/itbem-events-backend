package deliverypolicystore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/projectvault"
	"events-stocks/models"

	"github.com/gofrs/uuid"
)

const maximumStoredPolicyPatchBytes = 16 << 10

type ProjectRevisionInput struct {
	Level       deliverypolicy.Level
	Repository  string
	ChangeSetID string
	Patch       json.RawMessage
	Reason      string
	ExpiresAt   *time.Time
}

// BuildProjectRevision constructs one immutable, digest-sealed row for a
// project-scoped management API. The row remains a proposal: only a later
// independent decision can make it effective.
func BuildProjectRevision(organizationID string, projectID uuid.UUID, actor string, input ProjectRevisionInput, revisionID uuid.UUID, now time.Time) (models.DeliveryPolicyRevision, error) {
	organizationID, actor = strings.TrimSpace(organizationID), strings.TrimSpace(actor)
	if organizationID == "" || projectID == uuid.Nil || revisionID == uuid.Nil || actor == "" || len(actor) > 128 || now.IsZero() {
		return models.DeliveryPolicyRevision{}, fmt.Errorf("policy proposal identity is invalid")
	}
	patch, err := decodePolicyPatch(input.Patch)
	if err != nil {
		return models.DeliveryPolicyRevision{}, err
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return models.DeliveryPolicyRevision{}, fmt.Errorf("encode policy patch: %w", err)
	}

	repository, changeSetID := strings.TrimSpace(input.Repository), strings.TrimSpace(input.ChangeSetID)
	reason := strings.TrimSpace(input.Reason)
	expiresAt := utcTime(input.ExpiresAt)
	switch input.Level {
	case deliverypolicy.LevelProject:
		if repository != "" || changeSetID != "" || expiresAt != nil {
			return models.DeliveryPolicyRevision{}, fmt.Errorf("project policy carries a narrower scope")
		}
	case deliverypolicy.LevelRepository:
		repository, err = projectvault.CanonicalGitHubReference(repository)
		if err != nil || changeSetID != "" || expiresAt != nil {
			return models.DeliveryPolicyRevision{}, fmt.Errorf("repository policy scope is invalid")
		}
	case deliverypolicy.LevelOverride:
		if repository != "" {
			repository, err = projectvault.CanonicalGitHubReference(repository)
			if err != nil {
				return models.DeliveryPolicyRevision{}, fmt.Errorf("override repository scope is invalid")
			}
		}
		if changeSetID == "" || reason == "" || expiresAt == nil || !expiresAt.After(now.UTC()) || expiresAt.After(now.UTC().Add(24*time.Hour)) {
			return models.DeliveryPolicyRevision{}, fmt.Errorf("override requires an exact change set, reason, and expiry within 24 hours")
		}
	default:
		return models.DeliveryPolicyRevision{}, fmt.Errorf("project API cannot manage %s policy", input.Level)
	}

	projectIDCopy := projectID
	revision := models.DeliveryPolicyRevision{
		ID: revisionID, SchemaVersion: deliverypolicy.SchemaVersion, Level: string(input.Level),
		OrganizationID: organizationID, ProjectID: &projectIDCopy, RepositoryReference: repository,
		ChangeSetID: changeSetID, PatchJSON: string(patchJSON), Reason: reason, ExpiresAt: expiresAt,
		ProposedBy: actor, CreatedAt: now.UTC(),
	}
	layer, err := layerFromRevisionWithoutDigest(revision)
	if err != nil {
		return models.DeliveryPolicyRevision{}, err
	}
	revision.ContentSHA256, err = deliverypolicy.LayerDigest(layer)
	if err != nil {
		return models.DeliveryPolicyRevision{}, fmt.Errorf("digest policy proposal: %w", err)
	}
	if err := ValidateRevision(revision, now.UTC()); err != nil {
		return models.DeliveryPolicyRevision{}, err
	}
	return revision, nil
}

// ValidateRevision replays strict decoding, scope validation and digesting
// before an approval is appended. This keeps a database row from becoming
// authority merely because it exists.
func ValidateRevision(revision models.DeliveryPolicyRevision, now time.Time) error {
	layer, err := layerFromRevision(revision)
	if err != nil {
		return err
	}
	context := deliverypolicy.Context{
		OrganizationID: revision.OrganizationID,
		ProjectID:      projectIDString(revision.ProjectID),
		Repository:     revision.RepositoryReference,
		ChangeSetID:    revision.ChangeSetID,
	}
	if context.Repository == "" {
		context.Repository = "github://policy-validation/repository"
	}
	if context.ChangeSetID == "" {
		context.ChangeSetID = "policy-validation"
	}
	layer.Approved, layer.ApprovedBy, layer.ApprovedAt = true, "policy-validator", now.UTC()
	if _, err := deliverypolicy.Resolve(context, []deliverypolicy.Layer{layer}, now.UTC()); err != nil {
		return fmt.Errorf("policy revision is invalid: %w", err)
	}
	return nil
}

// ValidateDecision exposes the selector's decision invariants to the write
// boundary so malformed or self-approved evidence is rejected before insert.
func ValidateDecision(revision models.DeliveryPolicyRevision, decision models.DeliveryPolicyDecision, now time.Time) error {
	return validateDecision(revision, decision, now)
}

// ValidateDecisionTransition keeps one revision monotonic. Revoked content can
// never be reactivated; a corrected policy must receive a new revision/digest.
// Repeating the current state is allowed so the HTTP boundary can be idempotent.
func ValidateDecisionTransition(latest *models.DeliveryPolicyDecision, action string) error {
	switch action {
	case "approved":
		if latest != nil && latest.Action == "revoked" {
			return fmt.Errorf("a revoked policy revision cannot be reactivated")
		}
	case "revoked":
		if latest == nil {
			return fmt.Errorf("a pending policy revision has no authority to revoke")
		}
	default:
		return fmt.Errorf("policy decision action is invalid")
	}
	return nil
}

func decodePolicyPatch(raw json.RawMessage) (deliverypolicy.Patch, error) {
	if len(raw) == 0 || len(raw) > maximumStoredPolicyPatchBytes {
		return deliverypolicy.Patch{}, fmt.Errorf("policy patch is missing or too large")
	}
	var patch deliverypolicy.Patch
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return deliverypolicy.Patch{}, fmt.Errorf("policy patch is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return deliverypolicy.Patch{}, fmt.Errorf("policy patch contains trailing JSON")
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return deliverypolicy.Patch{}, fmt.Errorf("policy patch must be an object")
	}
	return patch, nil
}

func layerFromRevisionWithoutDigest(revision models.DeliveryPolicyRevision) (deliverypolicy.Layer, error) {
	var patch deliverypolicy.Patch
	decoder := json.NewDecoder(strings.NewReader(revision.PatchJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil || requireJSONEnd(decoder) != nil {
		return deliverypolicy.Layer{}, fmt.Errorf("policy revision patch is invalid")
	}
	return deliverypolicy.Layer{
		SchemaVersion: revision.SchemaVersion, RevisionID: revision.ID.String(), Level: deliverypolicy.Level(revision.Level),
		OrganizationID: revision.OrganizationID, ProjectID: projectIDString(revision.ProjectID),
		Repository: revision.RepositoryReference, ChangeSetID: revision.ChangeSetID,
		Patch: patch, Reason: revision.Reason, ExpiresAt: revision.ExpiresAt,
	}, nil
}

func projectIDString(projectID *uuid.UUID) string {
	if projectID == nil {
		return ""
	}
	return projectID.String()
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}
