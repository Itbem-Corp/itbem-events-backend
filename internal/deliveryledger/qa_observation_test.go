package deliveryledger

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/qaevidence"

	"github.com/gofrs/uuid"
)

func ledgerQAObservation() qaevidence.Observation {
	return qaevidence.Observation{
		SchemaVersion: qaevidence.SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64), PreviewPassed: true,
		RepositoryExecutionOrder: []string{"workspace://api", "workspace://web"},
		Repositories: []qaevidence.Repository{
			{Reference: "workspace://web", Branch: "itbem-agent/22222222-2222-4222-8222-222222222222", Commands: []qaevidence.Command{{Index: 1, Phase: "qa", Passed: true}, {Index: 0, Phase: "validation", Passed: true}}},
			{Reference: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", Commands: []qaevidence.Command{}},
		},
	}
}

func TestNewQAObservationEventCanonicalizesAndBindsTaskMatrix(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	observation := ledgerQAObservation()
	first, err := newQAObservationEvent(workItemID, observation, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	reordered := observation
	reordered.Repositories = []qaevidence.Repository{observation.Repositories[1], observation.Repositories[0]}
	second, err := newQAObservationEvent(workItemID, reordered, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.PayloadDigest != second.PayloadDigest || first.DedupeKey != second.DedupeKey || first.SubjectDigest != observation.MatrixDigest {
		t.Fatalf("equivalent QA evidence changed identity: %#v / %#v", first, second)
	}
}

func TestProjectQAObservationVerifiesEnvelopeAndPayload(t *testing.T) {
	event, err := newQAObservationEvent(uuid.Must(uuid.NewV4()), ledgerQAObservation(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 3
	projected, err := ProjectQAObservation(event)
	expected, _ := qaevidence.Canonical(ledgerQAObservation())
	if err != nil || !reflect.DeepEqual(projected.Observation, expected) {
		t.Fatalf("QA evidence projection failed: %#v / %v", projected, err)
	}
	encoded, _ := json.Marshal(projected)
	if strings.Contains(string(encoded), "command_output") {
		t.Fatalf("QA projection leaked private output: %s", encoded)
	}
	corrupted := event
	corrupted.PayloadJSON += " "
	if _, err := ProjectQAObservation(corrupted); err == nil {
		t.Fatal("corrupted QA ledger evidence was accepted")
	}
}
