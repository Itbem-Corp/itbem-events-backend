package deliveryledger

import (
	"strings"
	"testing"
	"time"

	"events-stocks/internal/securityevidence"

	"github.com/gofrs/uuid"
)

func ledgerSecurityObservation() securityevidence.Observation {
	return securityevidence.Observation{
		SchemaVersion: securityevidence.SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64),
		Repositories: []securityevidence.Repository{{Reference: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", SecretScanPassed: true}},
	}
}

func TestSecurityObservationEventBindsTaskAndMatrix(t *testing.T) {
	event, err := newSecurityObservationEvent(uuid.Must(uuid.NewV4()), ledgerSecurityObservation(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.ID, event.Sequence = uuid.Must(uuid.NewV4()), 2
	projected, err := ProjectSecurityObservation(event)
	if err != nil || projected.Observation.TaskID != ledgerSecurityObservation().TaskID || projected.Observation.MatrixDigest != ledgerSecurityObservation().MatrixDigest {
		t.Fatalf("security event projection failed: %#v / %v", projected, err)
	}
	corrupted := event
	corrupted.PayloadJSON += " "
	if _, err := ProjectSecurityObservation(corrupted); err == nil {
		t.Fatal("corrupted security event was accepted")
	}
}
