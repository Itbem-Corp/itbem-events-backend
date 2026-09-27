package automation

import (
	"events-stocks/configuration"
	"events-stocks/internal/inferencecapability"
	"events-stocks/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAutomationAttemptPolicyClaimCreatesSnapshotForNewRun(t *testing.T) {
	db, mock, cleanup := attemptPolicyClaimDB(t)
	defer cleanup()

	taskID := uuid.Must(uuid.NewV4())
	policyID := uuid.Must(uuid.NewV4())
	snapshotID := uuid.Must(uuid.NewV4())
	routes := []models.AutomationAIActionRoute{{Provider: "deepseek", Model: "deepseek-chat", ReasoningEnabled: true, ReasoningEffort: "low"}}
	routesJSON, routesHash, err := canonicalInferenceRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	policyTime := time.Now().UTC()
	createdAt := time.Now().UTC()
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID: snapshotID, AutomationTaskID: taskID, RunID: "run-created", Operation: "delivery.plan",
		PolicyRevision: 12, RoutesJSON: routesJSON, RoutesHash: routesHash, MaxCompletionTokens: 256, MaxInferenceCalls: inferenceAttemptCallQuota("delivery.plan"),
		CreatedAt: createdAt,
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		t.Fatal(err)
	}

	claimIdentity := testClaimIdentity()
	expectClaimWorkerIdentity(mock, taskID, "delivery.plan")
	expectNoStepAssignment(mock, taskID)
	mock.ExpectExec(`UPDATE "automation_tasks" SET .* WHERE id = \$[0-9]+ AND status = \$[0-9]+`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectClaimedTask(mock, taskID, "delivery.plan", "run-created", 256, nil)
	expectAttemptPolicyMissing(mock, taskID, "run-created")
	mock.ExpectQuery(`SELECT \* FROM "automation_ai_action_policies" WHERE operation = \$1 ORDER BY "automation_ai_action_policies"\."id" LIMIT \$2`).
		WithArgs("delivery.plan", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "operation", "provider", "model", "reasoning_enabled", "reasoning_effort", "routes_json", "revision", "updated_by", "created_at", "updated_at"}).
			AddRow(policyID.String(), "delivery.plan", "deepseek", "deepseek-chat", true, "low", routesJSON, int64(12), "root-user", policyTime, policyTime))
	mock.ExpectQuery(`INSERT INTO "automation_inference_attempt_policies".*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(snapshotID.String()))
	expectAttemptPolicyRow(mock, snapshot)
	expectParentReservationAbsent(mock, taskID)
	mock.ExpectCommit()

	response := claimAutomationTask(t, db, taskID, "run-created", claimIdentity)
	if response.Code != http.StatusNoContent {
		t.Fatalf("new run claim status=%d, want %d: %s; unmet SQL expectations: %v", response.Code, http.StatusNoContent, response.Body.String(), mock.ExpectationsWereMet())
	}
	key, _, ok := activeAttemptPolicySigningKey()
	if !ok {
		t.Fatal("test signing key is unavailable")
	}
	capability, err := inferencecapability.Verify(string(key), response.Header().Get(inferencecapability.HeaderName), taskID.String(), "run-created", "delivery.plan", time.Now().UTC())
	if err != nil || capability.WorkerID != testClaimIdentity().WorkerID || capability.AgentKey != "generalist" || capability.MachineID != testClaimIdentity().MachineID {
		t.Fatalf("successful claim response did not issue a server-signed capability bound to its owner: scope=%#v err=%v", capability, err)
	}
	if strings.Contains(response.Body.String(), response.Header().Get(inferencecapability.HeaderName)) {
		t.Fatal("claim capability must not be persisted in or duplicated into callback response body")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationAttemptPolicyClaimRenewsOnlyWithExistingSnapshot(t *testing.T) {
	db, mock, cleanup := attemptPolicyClaimDB(t)
	defer cleanup()

	taskID := uuid.Must(uuid.NewV4())
	routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}}
	routesJSON, routesHash, err := canonicalInferenceRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, RunID: "run-existing", Operation: "delivery.plan",
		PolicyRevision: 4, RoutesJSON: routesJSON, RoutesHash: routesHash, MaxCompletionTokens: 192, MaxInferenceCalls: inferenceAttemptCallQuota("delivery.plan"),
		CreatedAt: time.Now().UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		t.Fatal(err)
	}

	claimIdentity := testClaimIdentity()
	expectClaimWorkerIdentity(mock, taskID, "delivery.plan")
	expectNoStepAssignment(mock, taskID)
	mock.ExpectExec(`UPDATE "automation_tasks" SET .* WHERE id = \$[0-9]+ AND status = \$[0-9]+`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCurrentRunningTask(mock, taskID, "delivery.plan", "run-existing", 192, futureLease(), nil)
	expectAttemptPolicyRow(mock, snapshot)
	mock.ExpectExec(`UPDATE "automation_tasks" SET .* WHERE id = \$[0-9]+ AND status = \$[0-9]+ AND run_id = \$[0-9]+`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectParentReservationAbsent(mock, taskID)
	mock.ExpectCommit()

	response := claimAutomationTask(t, db, taskID, "run-existing", claimIdentity)
	if response.Code != http.StatusNoContent {
		t.Fatalf("same-run claim status=%d, want %d: %s; unmet SQL expectations: %v", response.Code, http.StatusNoContent, response.Body.String(), mock.ExpectationsWereMet())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationAttemptPolicyClaimDoesNotRenewLegacyRunWithoutSnapshot(t *testing.T) {
	db, mock, cleanup := attemptPolicyClaimDB(t)
	defer cleanup()

	taskID := uuid.Must(uuid.NewV4())
	claimIdentity := testClaimIdentity()
	expectClaimWorkerIdentity(mock, taskID, "delivery.plan")
	expectNoStepAssignment(mock, taskID)
	mock.ExpectExec(`UPDATE "automation_tasks" SET .* WHERE id = \$[0-9]+ AND status = \$[0-9]+`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectCurrentRunningTask(mock, taskID, "delivery.plan", "legacy-run", 192, futureLease(), nil)
	expectAttemptPolicyMissing(mock, taskID, "legacy-run")
	mock.ExpectCommit()

	response := claimAutomationTask(t, db, taskID, "legacy-run", claimIdentity)
	if response.Code != http.StatusConflict {
		t.Fatalf("legacy same-run claim status=%d, want %d: %s; unmet SQL expectations: %v", response.Code, http.StatusConflict, response.Body.String(), mock.ExpectationsWereMet())
	}
	if got := response.Header().Get("X-ITBEM-Automation-Run-Busy"); got != "1" {
		t.Fatalf("legacy run without snapshot must retain its queue delivery, busy header=%q", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func attemptPolicyClaimDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	t.Setenv(attemptPolicySigningKeyEnv, strings.Repeat("a", 48))
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	connection, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: connection}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	original := configuration.DB
	configuration.DB = db
	return db, mock, func() {
		configuration.DB = original
		connection.Close()
	}
}

func claimAutomationTask(t *testing.T, db *gorm.DB, taskID uuid.UUID, runID string, identity callbackRequest) *httptest.ResponseRecorder {
	t.Helper()
	_ = db
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/claim", nil), recorder)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: identity.AgentKey, MachineID: identity.MachineID})
	if err := claimAutomationTaskRun(ctx, taskID, runID, identity); err != nil {
		t.Fatalf("claim handler error: %v", err)
	}
	return recorder
}

func testClaimIdentity() callbackRequest {
	return callbackRequest{WorkerID: uuid.Must(uuid.FromString("a69b7f51-58b9-4f0e-aef3-1fbc23f79826")).String(), AgentKey: "generalist", MachineID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79827"}
}

func expectClaimWorkerIdentity(mock sqlmock.Sqlmock, taskID uuid.UUID, operation string) {
	identity := testClaimIdentity()
	mock.ExpectQuery(`SELECT .*"id".*"operation".*FROM "automation_tasks" WHERE .*"id" = \$1.*LIMIT \$[0-9]+`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "operation"}).AddRow(taskID.String(), operation))
	mock.ExpectQuery(`SELECT .* FROM "automation_agent_profiles" WHERE agent_key = \$1 AND active = \$2 ORDER BY "automation_agent_profiles"\."id" LIMIT \$3`).
		WithArgs(identity.AgentKey, true, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "agent_key", "name", "specialty", "description", "operations_json", "capabilities_json", "active", "created_at", "updated_at"}).
			AddRow(uuid.Must(uuid.NewV4()).String(), identity.AgentKey, "Generalist", "general", "test", `["delivery.plan"]`, `[]`, true, time.Now().UTC(), time.Now().UTC()))
}

func expectNoStepAssignment(mock sqlmock.Sqlmock, taskID uuid.UUID) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_assignments" WHERE child_automation_task_id = \$1 LIMIT \$2`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "execution_id", "delivery_plan_step_id", "child_automation_task_id", "target_machine_id", "target_agent_key", "status"}))
}

func expectClaimedTask(mock sqlmock.Sqlmock, taskID uuid.UUID, operation, runID string, maxTokens int, workItemID *uuid.UUID) {
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks" WHERE .*id.*= \$1.*LIMIT \$[0-9]+`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(automationTaskRows(taskID, operation, "running", runID, maxTokens, workItemID, futureLease()))
}

func expectCurrentRunningTask(mock sqlmock.Sqlmock, taskID uuid.UUID, operation, runID string, maxTokens int, lease *time.Time, workItemID *uuid.UUID) {
	mock.ExpectQuery(`SELECT \* FROM "automation_tasks" WHERE .*"id" = \$1.*LIMIT \$[0-9]+`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(automationTaskRows(taskID, operation, "running", runID, maxTokens, workItemID, lease))
}

func automationTaskRows(taskID uuid.UUID, operation, status, runID string, maxTokens int, workItemID *uuid.UUID, lease *time.Time) *sqlmock.Rows {
	identity := testClaimIdentity()
	var workItem any
	if workItemID != nil {
		workItem = workItemID.String()
	}
	return sqlmock.NewRows([]string{"id", "operation", "status", "run_id", "max_completion_tokens", "delivery_work_item_id", "lease_expires_at", "worker_id", "agent_key", "machine_id"}).
		AddRow(taskID.String(), operation, status, runID, maxTokens, workItem, lease, identity.WorkerID, identity.AgentKey, identity.MachineID)
}

func expectAttemptPolicyMissing(mock sqlmock.Sqlmock, taskID uuid.UUID, runID string) {
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_attempt_policies" WHERE automation_task_id = \$1 AND run_id = \$2 LIMIT \$3`).
		WithArgs(taskID.String(), runID, 1).
		WillReturnRows(sqlmock.NewRows(attemptPolicyColumns()))
}

func expectAttemptPolicyRow(mock sqlmock.Sqlmock, snapshot models.AutomationInferenceAttemptPolicy) {
	var projectID any
	if snapshot.ProjectID != nil {
		projectID = snapshot.ProjectID.String()
	}
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_attempt_policies" WHERE automation_task_id = \$1 AND run_id = \$2 LIMIT \$3`).
		WithArgs(snapshot.AutomationTaskID.String(), snapshot.RunID, 1).
		WillReturnRows(sqlmock.NewRows(attemptPolicyColumns()).AddRow(
			snapshot.ID.String(), snapshot.AutomationTaskID.String(), snapshot.RunID, snapshot.Operation, projectID,
			snapshot.PolicyRevision, snapshot.RoutesJSON, snapshot.RoutesHash, snapshot.MaxCompletionTokens, snapshot.MaxInferenceCalls,
			snapshot.SnapshotHash, snapshot.SignatureKeyID, snapshot.SnapshotSignature, snapshot.CreatedAt,
		))
}

func expectUnconfiguredAttemptPolicyCreation(mock sqlmock.Sqlmock, taskID uuid.UUID, runID, operation string, maxTokens int) {
	expectAttemptPolicyMissing(mock, taskID, runID)
	mock.ExpectQuery(`SELECT \* FROM "automation_ai_action_policies" WHERE operation = \$1 ORDER BY "automation_ai_action_policies"\."id" LIMIT \$2`).
		WithArgs(operation, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "operation", "provider", "model", "reasoning_enabled", "reasoning_effort", "routes_json", "revision", "updated_by", "created_at", "updated_at"}))
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, RunID: runID, Operation: operation,
		RoutesJSON: "[]", RoutesHash: sha256Hex([]byte("[]")), MaxCompletionTokens: maxTokens, MaxInferenceCalls: inferenceAttemptCallQuota(operation), CreatedAt: time.Now().UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		panic(err)
	}
	mock.ExpectQuery(`INSERT INTO "automation_inference_attempt_policies".*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(snapshot.ID.String()))
	expectAttemptPolicyRow(mock, snapshot)
}

func attemptPolicyColumns() []string {
	return []string{"id", "automation_task_id", "run_id", "operation", "project_id", "policy_revision", "routes_json", "routes_hash", "max_completion_tokens", "max_inference_calls", "snapshot_hash", "signature_key_id", "snapshot_signature", "created_at"}
}

func expectParentReservationAbsent(mock sqlmock.Sqlmock, taskID uuid.UUID) {
	mock.ExpectQuery(`SELECT .*parent_task_id.*FROM delivery_plan_step_assignments AS assignment.*WHERE assignment.child_automation_task_id = \$1 LIMIT \$2`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"parent_task_id"}))
}
