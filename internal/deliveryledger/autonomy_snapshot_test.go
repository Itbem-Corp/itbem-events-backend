package deliveryledger

import (
	"strings"
	"testing"
	"time"

	"events-stocks/internal/deliverypolicy"

	"github.com/gofrs/uuid"
)

func autonomySnapshotFixture(t *testing.T, mode deliverypolicy.GateApprovalMode) AutonomySnapshotInput {
	t.Helper()
	projectID := uuid.Must(uuid.NewV4())
	return AutonomySnapshotInput{
		ProjectID: projectID, ChangeSetID: "11111111-1111-4111-8111-111111111111",
		Repositories: []AutonomyRepository{{
			Repository: "github://Example/Service", SourceReference: "workspace://service", SourceRevision: strings.Repeat("a", 40),
			VaultRevisionID: uuid.Must(uuid.NewV4()).String(), VaultVersion: 2, VaultRevision: strings.Repeat("a", 40), VaultDigest: strings.Repeat("b", 64),
			Policy: deliverypolicy.ResolvedPolicy{Context: deliverypolicy.Context{ProjectID: projectID.String(), Repository: "github://Example/Service", ChangeSetID: "11111111-1111-4111-8111-111111111111"}, Resolved: true, GateApprovalMode: mode, Digest: strings.Repeat("c", 64)},
		}},
	}
}

func TestAutonomySnapshotCanonicalProjectionAndDelegation(t *testing.T) {
	input := autonomySnapshotFixture(t, deliverypolicy.GateApprovalDelegated)
	event, err := newAutonomySnapshotEvent(uuid.Must(uuid.NewV4()), input, time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 6
	projection, err := ProjectAutonomySnapshot(event)
	if err != nil || !projection.Delegated || len(projection.Repositories) != 1 || projection.Repositories[0].PolicyDigest != strings.Repeat("c", 64) {
		t.Fatalf("autonomy snapshot projection is invalid: %#v / %v", projection, err)
	}
	if strings.Contains(event.PayloadJSON, "private-cognito") {
		t.Fatalf("fixture unexpectedly contained private policy data: %s", event.PayloadJSON)
	}
}

func TestAutonomySnapshotRejectsTamperingAndUnresolvedPolicy(t *testing.T) {
	input := autonomySnapshotFixture(t, deliverypolicy.GateApprovalHuman)
	event, err := newAutonomySnapshotEvent(uuid.Must(uuid.NewV4()), input, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 1
	event.PayloadJSON = strings.Replace(event.PayloadJSON, `"source_revision":"`+strings.Repeat("a", 40)+`"`, `"source_revision":"`+strings.Repeat("d", 40)+`"`, 1)
	if _, err := ProjectAutonomySnapshot(event); err == nil {
		t.Fatal("tampered autonomy snapshot was accepted")
	}
	input = autonomySnapshotFixture(t, deliverypolicy.GateApprovalHuman)
	input.Repositories[0].Policy.Resolved = false
	if _, err := newAutonomySnapshotEvent(uuid.Must(uuid.NewV4()), input, time.Now().UTC()); err == nil {
		t.Fatal("unresolved policy was accepted as autonomous authority")
	}
}
