package automation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAutomationTraceTaskProjectionExcludesPrivateTaskFields(t *testing.T) {
	workItemID := uuid.Must(uuid.NewV4())
	completedAt := time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC)
	task := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: "private-user-id",
		DeliveryWorkItemID: &workItemID, CorrelationID: "trace-correlation", Operation: "delivery.qa",
		ProgressStep: "plan/step-1/validating", ProgressCall: 2, AttemptCount: 3,
		InputRef: "s3://private-bucket/input/secret-canary", OutputRef: "s3://private-bucket/output/secret-canary",
		Provider: "minimax", Model: "MiniMax-M3", ProviderResponseID: "provider-response-secret-canary",
		UsageJSON: `{"private_usage":"usage-secret-canary"}`, ErrorMessage: "raw-error-secret-canary",
		AIActionRoutesJSON: `[{"api_key":"route-secret-canary"}]`, RunID: "private-run-id",
		Status: "completed", CreatedAt: completedAt.Add(-time.Minute), UpdatedAt: completedAt, CompletedAt: &completedAt,
	}
	encoded, err := json.Marshal(automationTraceTaskFrom(task))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"input_ref", "output_ref", "s3://", "private-user-id", "provider_response_id", "provider-response-secret-canary",
		"usage-secret-canary", "raw-error-secret-canary", "route-secret-canary", "private-run-id", "api_key",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("trace task projection leaked %q: %s", forbidden, encoded)
		}
	}
	for _, expected := range []string{"delivery_work_item_id", "trace-correlation", "delivery.qa", "progress_step", "attempt_count", "completed_at"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("trace task projection omitted safe field %q: %s", expected, encoded)
		}
	}
}

func TestGetTraceSerializesOnlySafeTaskAndExecutionProjections(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	taskID, executionID, toolExecutionID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	completedAt := time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks" WHERE "automation_tasks"\."id" = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2`).
		WithArgs(taskID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "requested_by", "correlation_id", "operation", "input_ref", "output_ref", "provider", "model", "provider_response_id", "usage_json", "error_message", "ai_action_routes_json", "progress_step", "progress_call", "attempt_count", "status", "created_at", "updated_at", "completed_at"}).
			AddRow(taskID.String(), "private-user", "trace-correlation", "delivery.qa", "s3://private/input-secret", "s3://private/output-secret", "minimax", "MiniMax-M3", "provider-response-secret", `{"raw":"task-usage-secret"}`, "raw-task-error-secret", `[{"api_key":"route-secret"}]`, "plan/step-1/validating", 2, 3, "completed", completedAt.Add(-time.Minute), completedAt, completedAt))
	mock.ExpectQuery(`SELECT \* FROM "automation_executions" WHERE automation_task_id = \$1 ORDER BY completed_at ASC`).
		WithArgs(taskID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "step_key", "provider", "model", "input_tokens", "output_tokens", "total_tokens", "total_cost_micros", "pricing_basis", "pricing_snapshot_json", "usage_json", "request_ref", "response_ref", "completed_at"}).
			AddRow(executionID.String(), taskID.String(), "delivery.plan", "minimax", "MiniMax-M3", 100, 25, 125, 42, "snapshot", `{"private":"pricing-secret"}`, `{"_itbem_provider":{"finish_reason":"stop","ignored":"raw-usage-secret"}}`, "s3://private/request-secret", "s3://private/response-secret", completedAt))
	mock.ExpectQuery(`SELECT \* FROM "automation_tool_executions" WHERE automation_task_id = \$1 ORDER BY completed_at ASC`).
		WithArgs(taskID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "tool", "call_key", "call_status", "step_key", "provider", "model", "input_tokens", "output_tokens", "total_tokens", "total_cost_micros", "pricing_basis", "pricing_snapshot_json", "usage_json", "request_ref", "response_ref", "completed_at"}).
			AddRow(toolExecutionID.String(), taskID.String(), "stagehand", "qa-check", "completed", "delivery.qa", "minimax", "MiniMax-M3", 50, 10, 60, 20, "snapshot", `{"private":"tool-pricing-secret"}`, `{"_itbem_provider":{"status_code":200,"ignored":"tool-usage-secret"}}`, "s3://private/tool-request-secret", "s3://private/tool-response-secret", completedAt.Add(time.Second)))

	e := echo.New()
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/automation/tasks/"+taskID.String()+"/trace", nil), recorder)
	ctx.SetParamNames("id")
	ctx.SetParamValues(taskID.String())
	ctx.Set("cognito_sub", "private-user")
	if err := GetTrace(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("trace status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{
		"input_ref", "output_ref", "s3://private", "private-user", "provider-response-secret", "task-usage-secret",
		"raw-task-error-secret", "route-secret", "pricing-secret", "raw-usage-secret", "request_ref", "response_ref",
		"tool-pricing-secret", "tool-usage-secret", "tool-request-secret", "tool-response-secret", "ai_action_routes_json",
	} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("trace endpoint leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
	for _, expected := range []string{"trace-correlation", "delivery.qa", "delivery.plan", "stagehand", "stop"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("trace endpoint omitted safe trace value %q: %s", expected, recorder.Body.String())
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
