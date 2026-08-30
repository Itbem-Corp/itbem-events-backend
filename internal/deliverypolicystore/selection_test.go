package deliverypolicystore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/models"

	"github.com/gofrs/uuid"
)

var selectorNow = time.Date(2026, time.August, 30, 14, 0, 0, 0, time.UTC)

func selectorContext(t *testing.T) (deliverypolicy.Context, uuid.UUID) {
	t.Helper()
	projectID := uuid.Must(uuid.NewV4())
	return deliverypolicy.Context{
		OrganizationID: uuid.Must(uuid.NewV4()).String(), ProjectID: projectID.String(),
		Repository: "https://github.com/Example/service", ChangeSetID: "change-set:selector",
	}, projectID
}

func selectorRevision(t *testing.T, context deliverypolicy.Context, projectID uuid.UUID, level deliverypolicy.Level, patch deliverypolicy.Patch, repository string) models.DeliveryPolicyRevision {
	t.Helper()
	id := uuid.Must(uuid.NewV4())
	layer := deliverypolicy.Layer{SchemaVersion: deliverypolicy.SchemaVersion, RevisionID: id.String(), Level: level, Patch: patch}
	revision := models.DeliveryPolicyRevision{ID: id, SchemaVersion: deliverypolicy.SchemaVersion, Level: string(level), PatchJSON: mustJSON(t, patch), ProposedBy: "proposer", CreatedAt: selectorNow.Add(-3 * time.Hour)}
	switch level {
	case deliverypolicy.LevelOrganization:
		layer.OrganizationID, revision.OrganizationID = context.OrganizationID, context.OrganizationID
	case deliverypolicy.LevelProject:
		layer.OrganizationID, layer.ProjectID = context.OrganizationID, context.ProjectID
		revision.OrganizationID, revision.ProjectID = context.OrganizationID, &projectID
	case deliverypolicy.LevelRepository:
		layer.OrganizationID, layer.ProjectID, layer.Repository = context.OrganizationID, context.ProjectID, repository
		revision.OrganizationID, revision.ProjectID, revision.RepositoryReference = context.OrganizationID, &projectID, repository
	case deliverypolicy.LevelOverride:
		expires := selectorNow.Add(6 * time.Hour)
		layer.OrganizationID, layer.ProjectID, layer.Repository, layer.ChangeSetID = context.OrganizationID, context.ProjectID, repository, context.ChangeSetID
		layer.Reason, layer.ExpiresAt = "bounded exception", &expires
		revision.OrganizationID, revision.ProjectID, revision.RepositoryReference, revision.ChangeSetID = context.OrganizationID, &projectID, repository, context.ChangeSetID
		revision.Reason, revision.ExpiresAt = layer.Reason, &expires
	}
	digest, err := deliverypolicy.LayerDigest(layer)
	if err != nil {
		t.Fatal(err)
	}
	revision.ContentSHA256 = digest
	return revision
}

func selectorDecision(revision models.DeliveryPolicyRevision, action, actor string, occurredAt time.Time) models.DeliveryPolicyDecision {
	reason := ""
	if action == "revoked" {
		reason = "superseded safely"
	}
	return models.DeliveryPolicyDecision{
		ID: uuid.Must(uuid.NewV4()), PolicyRevisionID: revision.ID, PolicyDigest: revision.ContentSHA256,
		Action: action, Reason: reason, ActorCognitoSub: actor, OccurredAt: occurredAt,
	}
}

func completeMergePatch(branch string) deliverypolicy.Patch {
	mode, method := deliverypolicy.ModeMerge, "squash"
	tests, branches := []string{"unit"}, []string{branch}
	return deliverypolicy.Patch{Mode: &mode, MergeMethod: &method, RequiredTestKinds: &tests, AllowedTargetBranches: &branches}
}

func TestResolveEffectiveKeepsApprovedPolicyWhenNewProposalIsPending(t *testing.T) {
	context, projectID := selectorContext(t)
	approved := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("trunk"), "")
	pending := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("next"), "")
	policy, err := ResolveEffective(context, []models.DeliveryPolicyRevision{pending, approved}, []models.DeliveryPolicyDecision{
		selectorDecision(approved, "approved", "independent-reviewer", selectorNow.Add(-time.Hour)),
	}, selectorNow)
	if err != nil || !policy.Resolved || !policy.AllowsTargetBranch("trunk") || policy.AllowsTargetBranch("next") {
		t.Fatalf("pending proposal changed effective policy: %#v / %v", policy, err)
	}
}

func TestResolveEffectiveRevocationDoesNotReviveOlderRevision(t *testing.T) {
	context, projectID := selectorContext(t)
	older := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("old"), "")
	newer := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("new"), "")
	decisions := []models.DeliveryPolicyDecision{
		selectorDecision(older, "approved", "reviewer-a", selectorNow.Add(-3*time.Hour)),
		selectorDecision(newer, "approved", "reviewer-b", selectorNow.Add(-2*time.Hour)),
		selectorDecision(newer, "revoked", "release-owner", selectorNow.Add(-time.Hour)),
	}
	policy, err := ResolveEffective(context, []models.DeliveryPolicyRevision{older, newer}, decisions, selectorNow)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Resolved || policy.AllowsTargetBranch("old") || !containsMissing(policy.Missing, "mode") {
		t.Fatalf("revocation silently revived older policy: %#v", policy)
	}
}

func TestResolveEffectivePrefersExactRepositoryOverrideAndRevocationBlocksGlobalFallback(t *testing.T) {
	context, projectID := selectorContext(t)
	project := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("project"), "")
	globalBranches, exactBranches := []string{"global"}, []string{"exact"}
	global := selectorRevision(t, context, projectID, deliverypolicy.LevelOverride, deliverypolicy.Patch{AllowedTargetBranches: &globalBranches}, "")
	exact := selectorRevision(t, context, projectID, deliverypolicy.LevelOverride, deliverypolicy.Patch{AllowedTargetBranches: &exactBranches}, context.Repository)
	decisions := []models.DeliveryPolicyDecision{
		selectorDecision(project, "approved", "reviewer-project", selectorNow.Add(-3*time.Hour)),
		selectorDecision(global, "approved", "reviewer-global", selectorNow.Add(-2*time.Hour)),
		selectorDecision(exact, "approved", "reviewer-exact", selectorNow.Add(-time.Hour)),
	}
	policy, err := ResolveEffective(context, []models.DeliveryPolicyRevision{global, project, exact}, decisions, selectorNow)
	if err != nil || !policy.AllowsTargetBranch("exact") || policy.AllowsTargetBranch("global") {
		t.Fatalf("exact override did not win: %#v / %v", policy, err)
	}
	decisions = append(decisions, selectorDecision(exact, "revoked", "release-owner", selectorNow.Add(-time.Minute)))
	policy, err = ResolveEffective(context, []models.DeliveryPolicyRevision{exact, global, project}, decisions, selectorNow)
	if err != nil || !policy.AllowsTargetBranch("project") || policy.AllowsTargetBranch("global") {
		t.Fatalf("revoked exact override fell back to global override: %#v / %v", policy, err)
	}
}

func TestResolveEffectiveIsInputOrderIndependent(t *testing.T) {
	context, projectID := selectorContext(t)
	project := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("develop"), "")
	repositoryTests := []string{"contract", "unit"}
	repository := selectorRevision(t, context, projectID, deliverypolicy.LevelRepository, deliverypolicy.Patch{RequiredTestKinds: &repositoryTests}, context.Repository)
	decisions := []models.DeliveryPolicyDecision{
		selectorDecision(project, "approved", "reviewer-project", selectorNow.Add(-2*time.Hour)),
		selectorDecision(repository, "approved", "reviewer-repository", selectorNow.Add(-time.Hour)),
	}
	first, err := ResolveEffective(context, []models.DeliveryPolicyRevision{project, repository}, decisions, selectorNow)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveEffective(context, []models.DeliveryPolicyRevision{repository, project}, []models.DeliveryPolicyDecision{decisions[1], decisions[0]}, selectorNow)
	if err != nil || first.Digest != second.Digest || strings.Join(first.RequiredTestKinds, ",") != "contract,unit" {
		t.Fatalf("effective selection changed with input order: %#v / %#v / %v", first, second, err)
	}
}

func TestResolveEffectiveRejectsTamperingUnknownFieldsAndSelfApproval(t *testing.T) {
	context, projectID := selectorContext(t)
	valid := selectorRevision(t, context, projectID, deliverypolicy.LevelProject, completeMergePatch("main"), "")
	cases := []struct {
		name     string
		revision models.DeliveryPolicyRevision
		decision models.DeliveryPolicyDecision
	}{
		{name: "digest tampering", revision: func() models.DeliveryPolicyRevision {
			changed := valid
			changed.ContentSHA256 = strings.Repeat("a", 64)
			return changed
		}()},
		{name: "unknown patch field", revision: func() models.DeliveryPolicyRevision {
			changed := valid
			changed.PatchJSON = `{"mode":"merge","unknown":true}`
			return changed
		}()},
		{name: "self approval", revision: valid, decision: selectorDecision(valid, "approved", valid.ProposedBy, selectorNow.Add(-time.Hour))},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := test.decision
			if decision.ID == uuid.Nil {
				decision = selectorDecision(test.revision, "approved", "reviewer", selectorNow.Add(-time.Hour))
			}
			if _, err := ResolveEffective(context, []models.DeliveryPolicyRevision{test.revision}, []models.DeliveryPolicyDecision{decision}, selectorNow); err == nil {
				t.Fatal("unsafe persisted policy was accepted")
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func containsMissing(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
