// Package deliverypolicystore reconstructs effective policy from append-only
// persistence rows. It performs no database, GitHub, queue, merge, or release
// operation; callers remain responsible for querying only authorized data.
package deliverypolicystore

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/internal/projectvault"
	"events-stocks/models"

	"github.com/gofrs/uuid"
)

type decidedRevision struct {
	revision models.DeliveryPolicyRevision
	layer    deliverypolicy.Layer
	decision models.DeliveryPolicyDecision
}

// ResolveEffective selects at most one active revision per hierarchy level and
// delegates all final validation/digesting to deliverypolicy.Resolve. Pending
// proposals have no effect. Within one exact scope, the latest decision wins;
// a revocation therefore suppresses the scope instead of reviving older policy.
func ResolveEffective(context deliverypolicy.Context, revisions []models.DeliveryPolicyRevision, decisions []models.DeliveryPolicyDecision, now time.Time) (deliverypolicy.ResolvedPolicy, error) {
	canonicalRepository, err := projectvault.CanonicalGitHubReference(context.Repository)
	if err != nil || now.IsZero() {
		return deliverypolicy.ResolvedPolicy{}, fmt.Errorf("effective policy context is invalid")
	}
	context.Repository = canonicalRepository

	relevant := make(map[uuid.UUID]decidedRevision)
	for _, revision := range revisions {
		if !revisionApplies(context, revision) {
			continue
		}
		layer, layerErr := layerFromRevision(revision)
		if layerErr != nil {
			return deliverypolicy.ResolvedPolicy{}, layerErr
		}
		relevant[revision.ID] = decidedRevision{revision: revision, layer: layer}
	}

	for _, decision := range decisions {
		candidate, present := relevant[decision.PolicyRevisionID]
		if !present {
			continue
		}
		if err := validateDecision(candidate.revision, decision, now.UTC()); err != nil {
			return deliverypolicy.ResolvedPolicy{}, err
		}
		if candidate.decision.ID == uuid.Nil || laterDecision(decision, candidate.decision) {
			candidate.decision = decision
			relevant[decision.PolicyRevisionID] = candidate
		}
	}

	byScope := map[string]decidedRevision{}
	for _, candidate := range relevant {
		if candidate.decision.ID == uuid.Nil {
			continue
		}
		key := scopeKey(candidate.revision)
		current, present := byScope[key]
		if !present || laterDecision(candidate.decision, current.decision) {
			byScope[key] = candidate
		}
	}

	selected := make([]decidedRevision, 0, 5)
	for _, key := range []string{
		string(deliverypolicy.LevelPlatform),
		string(deliverypolicy.LevelOrganization) + "|" + context.OrganizationID,
		string(deliverypolicy.LevelProject) + "|" + context.OrganizationID + "|" + context.ProjectID,
		string(deliverypolicy.LevelRepository) + "|" + context.OrganizationID + "|" + context.ProjectID + "|" + context.Repository,
	} {
		if candidate, present := byScope[key]; present && candidate.decision.Action == "approved" {
			selected = append(selected, candidate)
		}
	}

	exactOverrideKey := string(deliverypolicy.LevelOverride) + "|" + context.OrganizationID + "|" + context.ProjectID + "|" + context.ChangeSetID + "|" + context.Repository
	globalOverrideKey := string(deliverypolicy.LevelOverride) + "|" + context.OrganizationID + "|" + context.ProjectID + "|" + context.ChangeSetID + "|"
	if candidate, present := byScope[exactOverrideKey]; present {
		if candidate.decision.Action == "approved" {
			selected = append(selected, candidate)
		}
	} else if candidate, present := byScope[globalOverrideKey]; present && candidate.decision.Action == "approved" {
		selected = append(selected, candidate)
	}

	sort.Slice(selected, func(left, right int) bool { return selected[left].layer.Level < selected[right].layer.Level })
	layers := make([]deliverypolicy.Layer, 0, len(selected))
	for _, candidate := range selected {
		layer := candidate.layer
		layer.Approved = true
		layer.ApprovedBy = strings.TrimSpace(candidate.decision.ActorCognitoSub)
		layer.ApprovedAt = candidate.decision.OccurredAt.UTC()
		layers = append(layers, layer)
	}
	policy, err := deliverypolicy.Resolve(context, layers, now.UTC())
	if err != nil {
		return deliverypolicy.ResolvedPolicy{}, fmt.Errorf("resolve effective delivery policy: %w", err)
	}
	return policy, nil
}

func layerFromRevision(revision models.DeliveryPolicyRevision) (deliverypolicy.Layer, error) {
	if revision.ID == uuid.Nil {
		return deliverypolicy.Layer{}, fmt.Errorf("policy revision identity is invalid")
	}
	var patch deliverypolicy.Patch
	decoder := json.NewDecoder(strings.NewReader(revision.PatchJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return deliverypolicy.Layer{}, fmt.Errorf("policy revision patch is invalid")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return deliverypolicy.Layer{}, fmt.Errorf("policy revision patch is invalid")
	}
	projectID := ""
	if revision.ProjectID != nil {
		projectID = revision.ProjectID.String()
	}
	layer := deliverypolicy.Layer{
		SchemaVersion: revision.SchemaVersion, RevisionID: revision.ID.String(), Level: deliverypolicy.Level(strings.TrimSpace(revision.Level)),
		OrganizationID: strings.TrimSpace(revision.OrganizationID), ProjectID: projectID,
		Repository: strings.TrimSpace(revision.RepositoryReference), ChangeSetID: strings.TrimSpace(revision.ChangeSetID),
		Patch: patch, Reason: strings.TrimSpace(revision.Reason), ExpiresAt: revision.ExpiresAt,
		Digest: strings.ToLower(strings.TrimSpace(revision.ContentSHA256)),
	}
	digest, err := deliverypolicy.LayerDigest(layer)
	if err != nil || digest != layer.Digest {
		return deliverypolicy.Layer{}, fmt.Errorf("policy revision digest does not match immutable content")
	}
	return layer, nil
}

func validateDecision(revision models.DeliveryPolicyRevision, decision models.DeliveryPolicyDecision, now time.Time) error {
	actor := strings.TrimSpace(decision.ActorCognitoSub)
	if decision.ID == uuid.Nil || decision.PolicyRevisionID != revision.ID || decision.PolicyDigest != revision.ContentSHA256 ||
		(decision.Action != "approved" && decision.Action != "revoked") || actor == "" || decision.OccurredAt.IsZero() || decision.OccurredAt.After(now.Add(time.Minute)) {
		return fmt.Errorf("policy decision is invalid")
	}
	if decision.Action == "approved" && actor == strings.TrimSpace(revision.ProposedBy) {
		return fmt.Errorf("policy approval is not independent from its proposer")
	}
	if decision.Action == "revoked" && strings.TrimSpace(decision.Reason) == "" {
		return fmt.Errorf("policy revocation reason is required")
	}
	return nil
}

func revisionApplies(context deliverypolicy.Context, revision models.DeliveryPolicyRevision) bool {
	projectID := ""
	if revision.ProjectID != nil {
		projectID = revision.ProjectID.String()
	}
	repository := strings.TrimSpace(revision.RepositoryReference)
	if repository != "" {
		canonical, err := projectvault.CanonicalGitHubReference(repository)
		if err != nil {
			return false
		}
		repository = canonical
	}
	switch deliverypolicy.Level(strings.TrimSpace(revision.Level)) {
	case deliverypolicy.LevelPlatform:
		return true
	case deliverypolicy.LevelOrganization:
		return revision.OrganizationID == context.OrganizationID
	case deliverypolicy.LevelProject:
		return revision.OrganizationID == context.OrganizationID && projectID == context.ProjectID
	case deliverypolicy.LevelRepository:
		return revision.OrganizationID == context.OrganizationID && projectID == context.ProjectID && repository == context.Repository
	case deliverypolicy.LevelOverride:
		return revision.OrganizationID == context.OrganizationID && projectID == context.ProjectID && revision.ChangeSetID == context.ChangeSetID && (repository == "" || repository == context.Repository)
	default:
		return false
	}
}

func scopeKey(revision models.DeliveryPolicyRevision) string {
	projectID := ""
	if revision.ProjectID != nil {
		projectID = revision.ProjectID.String()
	}
	repository := strings.TrimSpace(revision.RepositoryReference)
	if repository != "" {
		repository, _ = projectvault.CanonicalGitHubReference(repository)
	}
	switch deliverypolicy.Level(revision.Level) {
	case deliverypolicy.LevelPlatform:
		return string(deliverypolicy.LevelPlatform)
	case deliverypolicy.LevelOrganization:
		return string(deliverypolicy.LevelOrganization) + "|" + revision.OrganizationID
	case deliverypolicy.LevelProject:
		return string(deliverypolicy.LevelProject) + "|" + revision.OrganizationID + "|" + projectID
	case deliverypolicy.LevelRepository:
		return string(deliverypolicy.LevelRepository) + "|" + revision.OrganizationID + "|" + projectID + "|" + repository
	case deliverypolicy.LevelOverride:
		return string(deliverypolicy.LevelOverride) + "|" + revision.OrganizationID + "|" + projectID + "|" + revision.ChangeSetID + "|" + repository
	default:
		return "unsupported"
	}
}

func laterDecision(candidate, current models.DeliveryPolicyDecision) bool {
	if !candidate.OccurredAt.Equal(current.OccurredAt) {
		return candidate.OccurredAt.After(current.OccurredAt)
	}
	return candidate.ID.String() > current.ID.String()
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}
