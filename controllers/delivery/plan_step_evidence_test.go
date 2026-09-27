package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func TestPlanStepEvidenceDTODoesNotExposeStorageCoordinates(t *testing.T) {
	record := models.DeliveryPlanStepEvidence{
		ID: uuid.Must(uuid.NewV4()), RequirementKey: "qa-report", FileName: "report.txt", ContentType: "text/plain",
		SizeBytes: 128, SHA256: strings.Repeat("a", 64), AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: uuid.Must(uuid.NewV4()).String(),
		AgentKey: "qa", MachineID: "private-machine", AgentInstanceID: uuid.Must(uuid.NewV4()), FencingToken: 4,
		Bucket: "private-bucket-marker", ObjectKey: "secret/key-marker", CreatedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(safePlanStepEvidenceDTO(record))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, forbidden := range []string{"private-bucket-marker", "secret/key-marker", "bucket", "object_key", "machine_id", "worker_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("evidence DTO leaked %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{`"source":"agent"`, `"automation_task_id"`, `"run_id"`, `"agent_instance_id"`, `"fencing_token"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("evidence DTO omitted %q: %s", expected, body)
		}
	}
}

func TestPlanStepEvidenceCursorIsBoundToStepAndFilters(t *testing.T) {
	planID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	scope := planStepEvidenceCursorScope(planID, stepID, "qa-report", "")
	cursor := deliveryPlanStepEvidenceCursor{Version: 1, Scope: scope, CreatedAt: time.Now().UTC(), ID: uuid.Must(uuid.NewV4()).String()}
	encoded := encodePlanStepEvidenceCursor(cursor)
	decoded, err := decodePlanStepEvidenceCursor(encoded, scope)
	if err != nil || decoded == nil || decoded.ID != cursor.ID {
		t.Fatalf("valid cursor failed to decode: %#v, %v", decoded, err)
	}
	if _, err := decodePlanStepEvidenceCursor(encoded, planStepEvidenceCursorScope(planID, stepID, "", "")); err == nil {
		t.Fatal("cursor was accepted after changing its requirement filter")
	}
	if _, err := planStepEvidencePageSize("101"); err == nil {
		t.Fatal("unbounded evidence page size was accepted")
	}
}
