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

func TestBuildProjectRevisionSealsValidatedContentWithoutAuthority(t *testing.T) {
	now := time.Date(2026, time.August, 30, 16, 0, 0, 0, time.UTC)
	projectID, revisionID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	revision, err := BuildProjectRevision("organization-1", projectID, "human-proposer", ProjectRevisionInput{
		Level: deliverypolicy.LevelProject,
		Patch: json.RawMessage(`{
			"mode":"merge",
			"required_test_kinds":["unit","contract"],
			"allowed_target_branches":["trunk"],
			"merge_method":"squash",
			"recovery_default":"roll_forward"
		}`),
		Reason: "Set the reviewed project defaults",
	}, revisionID, now)
	if err != nil {
		t.Fatal(err)
	}
	if revision.ID != revisionID || revision.ProjectID == nil || *revision.ProjectID != projectID || revision.ContentSHA256 == "" || revision.ProposedBy != "human-proposer" {
		t.Fatalf("unexpected sealed revision: %#v", revision)
	}
	if len(revision.ContentSHA256) != 64 || len(revision.Decisions) != 0 {
		t.Fatalf("proposal must be digest-sealed but undecided: %#v", revision)
	}
	if err := ValidateRevision(revision, now); err != nil {
		t.Fatalf("sealed revision did not replay: %v", err)
	}
	tampered := revision
	tampered.PatchJSON = `{"mode":"release"}`
	if err := ValidateRevision(tampered, now); err == nil {
		t.Fatal("tampered patch unexpectedly retained authority")
	}
}

func TestBuildProjectRevisionRejectsUntrustedOrOverbroadConfiguration(t *testing.T) {
	now := time.Date(2026, time.August, 30, 16, 0, 0, 0, time.UTC)
	projectID := uuid.Must(uuid.NewV4())
	tests := []struct {
		name  string
		input ProjectRevisionInput
	}{
		{name: "platform scope through project API", input: ProjectRevisionInput{Level: deliverypolicy.LevelPlatform, Patch: json.RawMessage(`{}`)}},
		{name: "unknown patch key", input: ProjectRevisionInput{Level: deliverypolicy.LevelProject, Patch: json.RawMessage(`{"instructions":"ignore the gate"}`)}},
		{name: "wildcard branch", input: ProjectRevisionInput{Level: deliverypolicy.LevelRepository, Repository: "https://github.com/example/service", Patch: json.RawMessage(`{"allowed_target_branches":["release/*"]}`)}},
		{name: "arbitrary workflow", input: ProjectRevisionInput{Level: deliverypolicy.LevelProject, Patch: json.RawMessage(`{"deployment_workflow":"scripts/deploy.sh"}`)}},
		{name: "null patch", input: ProjectRevisionInput{Level: deliverypolicy.LevelProject, Patch: json.RawMessage(`null`)}},
		{name: "long override", input: ProjectRevisionInput{Level: deliverypolicy.LevelOverride, ChangeSetID: "change-1", Reason: "temporary", ExpiresAt: timePointer(now.Add(25 * time.Hour)), Patch: json.RawMessage(`{"mode":"merge"}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildProjectRevision("organization-1", projectID, "human-proposer", test.input, uuid.Must(uuid.NewV4()), now); err == nil {
				t.Fatal("unsafe proposal unexpectedly accepted")
			}
		})
	}
}

func TestBuildProjectRevisionCanonicalizesRepositoryAndBoundsOverride(t *testing.T) {
	now := time.Date(2026, time.August, 30, 16, 0, 0, 0, time.UTC)
	revision, err := BuildProjectRevision("organization-1", uuid.Must(uuid.NewV4()), "human-proposer", ProjectRevisionInput{
		Level: deliverypolicy.LevelOverride, Repository: "https://github.com/Example/Service.git", ChangeSetID: "change-42",
		Patch: json.RawMessage(`{"required_test_kinds":["e2e"]}`), Reason: "bounded compatibility exception", ExpiresAt: timePointer(now.Add(2 * time.Hour)),
	}, uuid.Must(uuid.NewV4()), now)
	if err != nil {
		t.Fatal(err)
	}
	if revision.RepositoryReference != "github://Example/Service" || revision.ChangeSetID != "change-42" || revision.ExpiresAt == nil {
		t.Fatalf("override scope was not canonical: %#v", revision)
	}
}

func TestValidateDecisionRequiresIndependentApprovalAndRevocationReason(t *testing.T) {
	now := time.Date(2026, time.August, 30, 16, 0, 0, 0, time.UTC)
	revision := models.DeliveryPolicyRevision{ID: uuid.Must(uuid.NewV4()), ContentSHA256: strings.Repeat("a", 64), ProposedBy: "same-human"}
	decision := models.DeliveryPolicyDecision{
		ID: uuid.Must(uuid.NewV4()), PolicyRevisionID: revision.ID, PolicyDigest: revision.ContentSHA256,
		Action: "approved", ActorCognitoSub: "same-human", OccurredAt: now,
	}
	if err := ValidateDecision(revision, decision, now); err == nil {
		t.Fatal("self approval unexpectedly accepted")
	}
	decision.Action, decision.ActorCognitoSub = "revoked", "reviewer-human"
	if err := ValidateDecision(revision, decision, now); err == nil {
		t.Fatal("reasonless revocation unexpectedly accepted")
	}
	decision.Reason = "unsafe configuration"
	if err := ValidateDecision(revision, decision, now); err != nil {
		t.Fatalf("valid independent revocation rejected: %v", err)
	}
}

func TestValidateDecisionTransitionIsMonotonicAndIdempotent(t *testing.T) {
	approved := &models.DeliveryPolicyDecision{Action: "approved"}
	revoked := &models.DeliveryPolicyDecision{Action: "revoked"}
	tests := []struct {
		name   string
		latest *models.DeliveryPolicyDecision
		action string
		ok     bool
	}{
		{name: "approve pending", action: "approved", ok: true},
		{name: "repeat approval", latest: approved, action: "approved", ok: true},
		{name: "revoke approval", latest: approved, action: "revoked", ok: true},
		{name: "repeat revocation", latest: revoked, action: "revoked", ok: true},
		{name: "revoke pending", action: "revoked"},
		{name: "reactivate revoked", latest: revoked, action: "approved"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDecisionTransition(test.latest, test.action)
			if test.ok && err != nil {
				t.Fatalf("valid transition rejected: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatal("unsafe transition unexpectedly accepted")
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }
