package automationqueuerepository_test

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/services/outbox"
)

type targetedOutboxPayload struct {
	planStepID      string
	agentKey        string
	targetMachineID string
}

func (matcher targetedOutboxPayload) Match(value driver.Value) bool {
	var body struct {
		Payload struct {
			PlanStepID      string `json:"plan_step_id"`
			AgentKey        string `json:"agent_key"`
			TargetMachineID string `json:"target_machine_id"`
		} `json:"payload"`
	}
	raw, ok := value.(string)
	if !ok || json.Unmarshal([]byte(raw), &body) != nil {
		return false
	}
	return body.Payload.PlanStepID == matcher.planStepID && body.Payload.AgentKey == matcher.agentKey && body.Payload.TargetMachineID == matcher.targetMachineID
}

func TestEnqueueAutomationProcessPersistsTargetPlanStepInOutboxJSON(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}

	message := automationqueue.Message{SchemaVersion: 1, JobID: uuid.Must(uuid.NewV4()).String(), TenantCode: "itbem", Type: "ai.local.process"}
	message.Payload.TaskID = uuid.Must(uuid.NewV4()).String()
	message.Payload.Operation = "delivery.implementation"
	message.Payload.InputRef = "s3://itbem-ai-inputs-local/automation/inputs/task/input.json"
	message.Payload.Attempt = 1
	message.Payload.PlanStepID = uuid.Must(uuid.NewV4()).String()
	message.Payload.AgentKey = "backend-engineer"
	message.Payload.TargetMachineID = uuid.Must(uuid.NewV4()).String()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "outbox_events"`).WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		targetedOutboxPayload{planStepID: message.Payload.PlanStepID, agentKey: message.Payload.AgentKey, targetMachineID: message.Payload.TargetMachineID},
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
	mock.ExpectCommit()
	queued, err := outbox.EnqueueAutomationProcess(context.Background(), db, message)
	if err != nil || !queued {
		t.Fatalf("EnqueueAutomationProcess = %v, %v", queued, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
