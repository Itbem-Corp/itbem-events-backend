package automation

import (
	"regexp"
	"strings"
	"testing"

	"events-stocks/internal/automationagent"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRequiredPlanStepEvidenceMustMatchCurrentRunInstanceAndFence(t *testing.T) {
	for _, test := range []struct {
		name  string
		count int64
		want  error
	}{
		{name: "missing or stale evidence", count: 0, want: errPlanStepRequiredEvidenceMissing},
		{name: "current evidence", count: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			planID, stepID, taskID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			runID := uuid.Must(uuid.NewV4()).String()
			step := models.DeliveryPlanStep{ID: stepID, PlanID: planID, EvidenceRequirementsJSON: `[{"key":"report","title":"Report","required":true,"content_types":["text/plain"],"max_bytes":1024}]`}
			task := models.AutomationTask{ID: taskID}
			identity := automationagent.AgentIdentity{WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "qa", MachineID: uuid.Must(uuid.NewV4()).String()}
			fence := int64(14)
			query := regexp.QuoteMeta(`SELECT count(*) FROM "delivery_plan_step_evidences" WHERE plan_id = $1 AND plan_version = $2 AND step_id = $3 AND requirement_key = $4 AND automation_task_id = $5 AND run_id = $6 AND worker_id = $7 AND agent_key = $8 AND machine_id = $9 AND agent_instance_id = $10 AND fencing_token = $11`)
			mock.ExpectQuery(query).WithArgs(planID, 7, stepID, "report", taskID, runID, identity.WorkerID, identity.AgentKey, identity.MachineID, instanceID, fence).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.count))
			got := requirePlanStepEvidenceRequirements(db, step, 7, task, runID, identity, fence, instanceID)
			if got != test.want {
				t.Fatalf("required evidence result = %v; want %v", got, test.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("required evidence query did not bind every scope field: %v", err)
			}
		})
	}
}

func TestOptionalPlanStepEvidenceDoesNotBlockCompletion(t *testing.T) {
	requirements := `[{"key":"report","title":"Report","required":false,"content_types":["text/plain"],"max_bytes":1024}]`
	step := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: uuid.Must(uuid.NewV4()), EvidenceRequirementsJSON: requirements}
	task := models.AutomationTask{ID: uuid.Must(uuid.NewV4())}
	identity := automationagent.AgentIdentity{WorkerID: "worker", AgentKey: "qa", MachineID: "machine"}
	if err := requirePlanStepEvidenceRequirements(nil, step, 1, task, uuid.Must(uuid.NewV4()).String(), identity, 1, uuid.Must(uuid.NewV4())); err != nil {
		t.Fatalf("optional evidence should not block completion: %v", err)
	}
}

func TestStepEvidencePayloadValidationRejectsCredentialPatternsAndMIMEMismatch(t *testing.T) {
	if !validStepEvidenceContent([]byte("report complete"), "text/plain") {
		t.Fatal("plain UTF-8 report should be accepted")
	}
	if validStepEvidenceContent([]byte("<html>no</html>"), "text/plain") != true {
		t.Fatal("plain text may contain arbitrary report text; MIME safety is enforced at download with attachment and nosniff")
	}
	if validStepEvidenceContent([]byte("not-json"), "application/json") {
		t.Fatal("invalid JSON should be rejected")
	}
	if validStepEvidenceContent([]byte("plain text"), "image/png") {
		t.Fatal("MIME/magic mismatch should be rejected")
	}
	if !containsHighConfidencePatchSecret([]byte("token=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789")) {
		t.Fatal("high-confidence secret pattern was not detected")
	}
	if !stepEvidenceJSONSecretPattern.Match([]byte(`{"api_key":"abcdefghijklmnopqrstuvwxyz123456"}`)) {
		t.Fatal("JSON key/value secret pattern was not detected")
	}
	if validStepEvidenceFilename("../report.txt") || validStepEvidenceFilename("secret\r\n.txt") || !validStepEvidenceFilename("qa report.txt") {
		t.Fatal("filename allow-list did not reject traversal/control data or accept a safe display name")
	}
}

func TestPlanStepEvidenceObjectKeyUsesServerOnlyPrefixOutsideLocalAgentIAM(t *testing.T) {
	taskID := uuid.Must(uuid.NewV4())
	runID := uuid.Must(uuid.NewV4()).String()
	stepID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	key := planStepEvidenceObjectKey(deliveryPlanStepEvidenceUploadIdentity{
		TaskID: taskID, RunID: runID, StepID: stepID, EventID: eventID, SHA256: digest,
	})
	want := "delivery-step-evidence/" + taskID.String() + "/runs/" + runID + "/steps/" + stepID.String() + "/evidence/" + eventID.String() + "-" + digest
	if key != want {
		t.Fatalf("server evidence key = %q; want %q", key, want)
	}
	// The active local-agent policy in itbem-events-infrastructure/policies/
	// itbem-local-ai-agent-prod.json allows PutObject only for automation/*.
	// Keep server-verified evidence outside that worker-writable prefix.
	const localAgentPutObjectPrefix = "automation/"
	if !strings.HasPrefix(key, "delivery-step-evidence/") || strings.HasPrefix(key, localAgentPutObjectPrefix) {
		t.Fatalf("server evidence key %q overlaps local-agent PutObject allowlist %q", key, localAgentPutObjectPrefix)
	}
}
