package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliverypolicy"
	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func TestBuildEffectivePolicySnapshotOmitsPrivateActorIdentity(t *testing.T) {
	now := time.Date(2026, time.August, 30, 15, 0, 0, 0, time.UTC)
	projectID, vaultID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	policy := deliverypolicy.ResolvedPolicy{
		SchemaVersion: 1, Mode: deliverypolicy.ModeMerge, RequiredTestKinds: []string{"unit"}, AllowedTargetBranches: []string{"trunk"},
		RequiredHealthChecks: []string{}, RequiredPostMergeChecks: []string{}, Missing: []string{}, Resolved: true,
		Digest: strings.Repeat("a", 64), Safety: deliverypolicy.SafetyFloor{IndependentReview: true},
		Sources: []deliverypolicy.Source{{Level: deliverypolicy.LevelProject, RevisionID: "revision-1", Digest: strings.Repeat("b", 64), ApprovedBy: "private-cognito-sub", ApprovedAt: now.Add(-time.Hour)}},
	}
	vault := models.DeliveryProjectVaultRevision{ID: vaultID, Version: 3, Revision: strings.Repeat("c", 40), ContentSHA256: strings.Repeat("d", 64)}
	snapshot := buildEffectivePolicySnapshot(projectID, "github://example/service", "change-1", true, vault, policy, now)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, private := range []string{"private-cognito-sub", "approved_by", "proposed_by", "patch_json"} {
		if strings.Contains(value, private) {
			t.Fatalf("effective policy projection exposed %q: %s", private, value)
		}
	}
	if !strings.Contains(value, `"overrides_considered":true`) || !strings.Contains(value, `"repository_sha":"`+vault.Revision+`"`) || !strings.Contains(value, `"sources":[{"level":"project"`) {
		t.Fatalf("effective policy projection omitted safe evidence: %s", value)
	}
}

func TestBuildEffectivePolicySnapshotUsesExplicitEmptyCollections(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	snapshot := buildEffectivePolicySnapshot(projectID, "github://example/service", "", false, models.DeliveryProjectVaultRevision{}, deliverypolicy.ResolvedPolicy{}, time.Now())
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, collection := range []string{`"required_test_kinds":[]`, `"allowed_target_branches":[]`, `"required_secret_references":[]`, `"required_variable_references":[]`, `"required_health_checks":[]`, `"required_post_merge_checks":[]`, `"sources":[]`, `"missing":[]`} {
		if !strings.Contains(value, collection) {
			t.Fatalf("safe policy collection was not explicit (%s): %s", collection, value)
		}
	}
}
