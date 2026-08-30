package deliveryledger

import (
	"strings"
	"testing"
	"time"

	"events-stocks/internal/environmentevidence"

	"github.com/gofrs/uuid"
)

func ledgerEnvironmentObservation() environmentevidence.Observation {
	return environmentevidence.Observation{
		SchemaVersion: environmentevidence.SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64),
		Repositories: []environmentevidence.Repository{{
			Repository: "example/service", HeadSHA: strings.Repeat("b", 40), Workflow: ".github/workflows/deploy.yml", Environment: "production",
			RequiredSecretReferences: []string{"DATABASE_URL"}, RequiredVariableReferences: []string{}, WorkflowExists: true, EnvironmentExists: true,
			MissingSecretReferences: []string{}, MissingVariableReferences: []string{},
		}},
	}
}

func TestEnvironmentObservationEventCanonicalProjection(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	now := time.Date(2026, time.August, 30, 16, 0, 0, 0, time.UTC)
	event, err := newEnvironmentObservationEvent(workItemID, ledgerEnvironmentObservation(), now)
	if err != nil {
		t.Fatal(err)
	}
	if event.EventType != EventTypeEnvironmentObserved || event.SubjectDigest != strings.Repeat("a", 64) || !strings.Contains(event.DedupeKey, "environment-observation-v1") || strings.Contains(strings.ToLower(event.PayloadJSON), `"value"`) {
		t.Fatalf("unsafe environment event: %#v", event)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 7
	projection, err := ProjectEnvironmentObservation(event)
	if err != nil || projection.Sequence != 7 || !projection.Observation.Repositories[0].Ready() {
		t.Fatalf("environment event did not project: %#v / %v", projection, err)
	}
}

func TestEnvironmentObservationProjectionRejectsTampering(t *testing.T) {
	event, err := newEnvironmentObservationEvent(uuid.Must(uuid.NewV4()), ledgerEnvironmentObservation(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 1
	event.PayloadJSON = strings.Replace(event.PayloadJSON, `"workflow_exists":true`, `"workflow_exists":false`, 1)
	if _, err := ProjectEnvironmentObservation(event); err == nil {
		t.Fatal("tampered environment event was accepted")
	}
}
