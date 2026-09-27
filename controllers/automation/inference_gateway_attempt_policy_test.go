package automation

import (
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/inferencecapability"
	"events-stocks/models"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newGatewayAttemptPolicySnapshot(t *testing.T, taskID uuid.UUID, runID, operation string, projectID *uuid.UUID, maxTokens int, revision int64, routes []models.AutomationAIActionRoute) models.AutomationInferenceAttemptPolicy {
	t.Helper()
	t.Setenv(attemptPolicySigningKeyEnv, strings.Repeat("a", 48))
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	routesJSON, routesHash, err := canonicalInferenceRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID:                  uuid.Must(uuid.NewV4()),
		AutomationTaskID:    taskID,
		RunID:               runID,
		Operation:           operation,
		ProjectID:           projectID,
		PolicyRevision:      revision,
		RoutesJSON:          routesJSON,
		RoutesHash:          routesHash,
		MaxCompletionTokens: maxTokens,
		MaxInferenceCalls:   inferenceAttemptCallQuota(operation),
		CreatedAt:           time.Now().UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func expectGatewayAttemptPolicyLookup(mock sqlmock.Sqlmock, snapshot models.AutomationInferenceAttemptPolicy) {
	projectValue := any(nil)
	if snapshot.ProjectID != nil {
		projectValue = snapshot.ProjectID.String()
	}
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_attempt_policies" WHERE automation_task_id = \$1 AND run_id = \$2 LIMIT \$3`).
		WithArgs(snapshot.AutomationTaskID.String(), snapshot.RunID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "run_id", "operation", "project_id", "policy_revision", "routes_json", "routes_hash", "max_completion_tokens", "max_inference_calls", "snapshot_hash", "signature_key_id", "snapshot_signature", "created_at"}).
			AddRow(snapshot.ID.String(), snapshot.AutomationTaskID.String(), snapshot.RunID, snapshot.Operation, projectValue, snapshot.PolicyRevision, snapshot.RoutesJSON, snapshot.RoutesHash, snapshot.MaxCompletionTokens, snapshot.MaxInferenceCalls, snapshot.SnapshotHash, snapshot.SignatureKeyID, snapshot.SnapshotSignature, snapshot.CreatedAt))
}

func setupGatewayAttemptPolicyDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	t.Setenv(attemptPolicySigningKeyEnv, strings.Repeat("a", 48))
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() {
		configuration.DB = previousDB
		_ = sqlDB.Close()
	})
	return mock
}

func expectGatewayLeasedTask(mock sqlmock.Sqlmock, taskID, workItemID *uuid.UUID, operation, runID string, maxTokens int) {
	expectGatewayLeasedTaskWithIdentity(mock, taskID, workItemID, operation, runID, maxTokens, "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", "generalist", "b69b7f51-58b9-4f0e-aef3-1fbc23f79827") // gitleaks:allow synthetic test-only security canary; never used for provider access
}

func expectGatewayLeasedTaskWithIdentity(mock sqlmock.Sqlmock, taskID, workItemID *uuid.UUID, operation, runID string, maxTokens int, workerID, agentKey, machineID string) {
	var workItemValue any
	if workItemID != nil {
		workItemValue = workItemID.String()
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT .* FROM "automation_tasks" WHERE .*"id" = \$1`).
		WithArgs(taskID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "operation", "status", "run_id", "lease_expires_at", "max_completion_tokens", "delivery_work_item_id", "worker_id", "agent_key", "machine_id"}).
			AddRow(taskID.String(), operation, "running", runID, time.Now().UTC().Add(5*time.Minute), maxTokens, workItemValue, workerID, agentKey, machineID))
}

func TestGatewayInferenceLeaseRequiresCurrentCapabilityIdentityAndRun(t *testing.T) {
	for _, scenario := range []struct {
		name, databaseRunID, databaseWorkerID string
		wantValid                             bool
	}{
		{name: "owner matches", databaseRunID: "run-current", databaseWorkerID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", wantValid: true},
		{name: "new run revokes old token", databaseRunID: "run-next", databaseWorkerID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79826"},
		{name: "different worker cannot reuse token", databaseRunID: "run-current", databaseWorkerID: "c69b7f51-58b9-4f0e-aef3-1fbc23f79826"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mock := setupGatewayAttemptPolicyDB(t)
			taskID := uuid.Must(uuid.NewV4())
			capabilityScope := inferencecapability.Scope{
				TaskID: taskID.String(), RunID: "run-current", Operation: "delivery.plan",
				WorkerID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist", MachineID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79827",
			}
			expectGatewayLeasedTaskWithIdentity(mock, &taskID, nil, "delivery.plan", scenario.databaseRunID, 100, scenario.databaseWorkerID, "generalist", "b69b7f51-58b9-4f0e-aef3-1fbc23f79827")
			if scenario.wantValid {
				routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}}
				snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-current", "delivery.plan", nil, 100, 2, routes)
				expectGatewayAttemptPolicyLookup(mock, snapshot)
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{
				TaskID: taskID.String(), RunID: "run-current", Operation: "delivery.plan", MaxCompletionTokens: 80,
			}, capabilityScope)
			if scenario.wantValid {
				if err != nil || !configured || len(scope.Routes) != 1 {
					t.Fatalf("current capability scope rejected: %#v configured=%v err=%v", scope, configured, err)
				}
			} else if err == nil || configured || len(scope.Routes) != 0 {
				t.Fatalf("revoked or foreign worker capability accepted: scope=%#v configured=%v err=%v", scope, configured, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayInferencePlanStepRequiresExactLiveLease(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		stepWorkerID     string
		stepLeaseExpired bool
		wantAccept       bool
	}{
		{name: "approved live lease matches", wantAccept: true},
		{name: "step is leased to another worker", stepWorkerID: "c69b7f51-58b9-4f0e-aef3-1fbc23f79826"},
		{name: "step lease expired", stepLeaseExpired: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			mock := setupGatewayAttemptPolicyDB(t)
			taskID, workItemID, stepID, planID, approvalID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			runID := uuid.Must(uuid.NewV4()).String()
			workerID, agentKey, machineID := "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", "implementation_specialist", "b69b7f51-58b9-4f0e-aef3-1fbc23f79827"
			stepWorkerID := scenario.stepWorkerID
			if stepWorkerID == "" {
				stepWorkerID = workerID
			}
			now := time.Now().UTC()
			taskExpiry := now.Add(5 * time.Minute)
			stepExpiry := now.Add(5 * time.Minute)
			if scenario.stepLeaseExpired {
				stepExpiry = now.Add(-time.Second)
			}
			stepTaskID := taskID.String()
			task := models.AutomationTask{
				ID: taskID, Operation: "delivery.implementation", RunID: runID, Status: "running", WorkerID: workerID, AgentKey: agentKey,
				MachineID: machineID, DeliveryWorkItemID: &workItemID, LeaseExpiresAt: &taskExpiry,
			}
			request := inferenceRequest{TaskID: taskID.String(), RunID: runID, Operation: task.Operation, PlanStepID: stepID.String()}
			capability := inferencecapability.Scope{TaskID: taskID.String(), RunID: runID, Operation: task.Operation, WorkerID: workerID, AgentKey: agentKey, MachineID: machineID}

			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE id = \$1 ORDER BY "delivery_plan_steps"\."id" LIMIT \$2 FOR UPDATE`).
				WithArgs(stepID.String(), 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "plan_id", "status", "automation_task_id", "run_id", "worker_id", "agent_key", "machine_id", "lease_fence", "lease_expires_at"}).
					AddRow(stepID.String(), planID.String(), models.DeliveryPlanStepRunning, stepTaskID, runID, stepWorkerID, agentKey, machineID, 7, stepExpiry))
			mock.ExpectQuery(`SELECT "id","work_item_id","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2 FOR SHARE`).
				WithArgs(planID.String(), 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "status", "approved_gate_id"}).AddRow(planID.String(), workItemID.String(), "approved", approvalID.String()))
			mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_assignments" WHERE child_automation_task_id = \$1 ORDER BY "delivery_plan_step_assignments"\."id" LIMIT \$2 FOR UPDATE`).
				WithArgs(taskID.String(), 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			if scenario.wantAccept {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := configuration.DB.Transaction(func(tx *gorm.DB) error {
				return validateGatewayInferencePlanStep(tx, task, request, capability, stepID)
			})
			if scenario.wantAccept && err != nil {
				t.Fatalf("valid active step lease rejected: %v", err)
			}
			if !scenario.wantAccept && err == nil {
				t.Fatal("mismatched or stale plan-step lease was accepted")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayInferenceKeepsClaimedRouteAfterLivePolicyChanges(t *testing.T) {
	mock := setupGatewayAttemptPolicyDB(t)
	taskID := uuid.Must(uuid.NewV4())
	projectID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	claimedRoute := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3", ReasoningEnabled: true, ReasoningEffort: "medium"}}
	// The action policy has since been edited to a different provider/model.
	// Only the immutable attempt snapshot is queried by the gateway.
	currentPolicyRoute := models.AutomationAIActionRoute{Provider: "deepseek", Model: "deepseek-chat"}
	if currentPolicyRoute.Provider == claimedRoute[0].Provider && currentPolicyRoute.Model == claimedRoute[0].Model {
		t.Fatal("test fixture must represent a live policy change")
	}
	snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-frozen", "delivery.implementation", &projectID, 120, 8, claimedRoute)
	expectGatewayLeasedTask(mock, &taskID, &workItemID, "delivery.implementation", "run-frozen", 120)
	mock.ExpectQuery(`SELECT .* FROM "delivery_work_items" WHERE .*"id" = \$1`).
		WithArgs(workItemID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(projectID.String()))
	expectGatewayAttemptPolicyLookup(mock, snapshot)
	mock.ExpectCommit()

	scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{
		TaskID: taskID.String(), RunID: "run-frozen", Operation: "delivery.implementation", MaxCompletionTokens: 75,
	})
	if err != nil || !configured || len(scope.Routes) != 1 || scope.Routes[0] != claimedRoute[0] || scope.PolicyRevision != 8 || scope.RoutesHash != snapshot.RoutesHash {
		t.Fatalf("claimed attempt route changed with live policy: scope=%#v configured=%v err=%v", scope, configured, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayInferenceRejectsMissingAttemptSnapshot(t *testing.T) {
	mock := setupGatewayAttemptPolicyDB(t)
	taskID := uuid.Must(uuid.NewV4())
	expectGatewayLeasedTask(mock, &taskID, nil, "delivery.plan", "run-missing", 100)
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_attempt_policies" WHERE automation_task_id = \$1 AND run_id = \$2 LIMIT \$3`).
		WithArgs(taskID.String(), "run-missing", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "run_id", "operation", "project_id", "policy_revision", "routes_json", "routes_hash", "max_completion_tokens", "max_inference_calls", "snapshot_hash", "signature_key_id", "snapshot_signature", "created_at"}))
	mock.ExpectRollback()

	scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{
		TaskID: taskID.String(), RunID: "run-missing", Operation: "delivery.plan", MaxCompletionTokens: 20,
	})
	if err == nil || configured || len(scope.Routes) != 0 {
		t.Fatalf("missing attempt snapshot was not rejected: scope=%#v configured=%v err=%v", scope, configured, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayInferenceRejectsAttemptSnapshotProjectOrOperationMismatch(t *testing.T) {
	tests := []struct {
		name            string
		workItem        bool
		taskOperation   string
		snapshotOp      string
		projectMismatch bool
	}{
		{name: "project mismatch", workItem: true, taskOperation: "delivery.implementation", snapshotOp: "delivery.implementation", projectMismatch: true},
		{name: "operation mismatch", taskOperation: "delivery.plan", snapshotOp: "delivery.qa"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := setupGatewayAttemptPolicyDB(t)
			taskID := uuid.Must(uuid.NewV4())
			var workItemID, derivedProjectID, snapshotProjectID *uuid.UUID
			if tt.workItem {
				workItem := uuid.Must(uuid.NewV4())
				project := uuid.Must(uuid.NewV4())
				workItemID, derivedProjectID = &workItem, &project
				if tt.projectMismatch {
					otherProject := uuid.Must(uuid.NewV4())
					snapshotProjectID = &otherProject
				} else {
					snapshotProjectID = &project
				}
			}
			routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}}
			snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-scope", tt.snapshotOp, snapshotProjectID, 100, 3, routes)
			expectGatewayLeasedTask(mock, &taskID, workItemID, tt.taskOperation, "run-scope", 100)
			if workItemID != nil {
				mock.ExpectQuery(`SELECT .* FROM "delivery_work_items" WHERE .*"id" = \$1`).
					WithArgs(workItemID.String(), 1).
					WillReturnRows(sqlmock.NewRows([]string{"project_id"}).AddRow(derivedProjectID.String()))
			}
			expectGatewayAttemptPolicyLookup(mock, snapshot)
			mock.ExpectRollback()

			scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{
				TaskID: taskID.String(), RunID: "run-scope", Operation: tt.taskOperation, MaxCompletionTokens: 40,
			})
			if err == nil || configured || len(scope.Routes) != 0 {
				t.Fatalf("mismatched snapshot scope was accepted: scope=%#v configured=%v err=%v", scope, configured, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGatewayInferenceRejectsRehashedTamperedAttemptSnapshot(t *testing.T) {
	mock := setupGatewayAttemptPolicyDB(t)
	taskID := uuid.Must(uuid.NewV4())
	routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}}
	snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-altered", "delivery.plan", nil, 100, 3, routes)
	// Model a privileged database writer who can alter the recipe and recompute
	// every unkeyed hash. The unchanged control-plane HMAC must still fail.
	snapshot.RoutesJSON, snapshot.RoutesHash, _ = canonicalInferenceRoutes([]models.AutomationAIActionRoute{{Provider: "deepseek", Model: "deepseek-chat"}})
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if _, _, valid := validateAutomationInferenceAttemptPolicy(snapshot); !valid {
		t.Fatal("tampered fixture must be internally consistent after recomputing unkeyed hashes")
	}
	expectGatewayLeasedTask(mock, &taskID, nil, "delivery.plan", "run-altered", 100)
	expectGatewayAttemptPolicyLookup(mock, snapshot)
	mock.ExpectRollback()

	scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{
		TaskID: taskID.String(), RunID: "run-altered", Operation: "delivery.plan", MaxCompletionTokens: 40,
	})
	if err == nil || configured || len(scope.Routes) != 0 {
		t.Fatalf("altered attempt snapshot was accepted: scope=%#v configured=%v err=%v", scope, configured, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayInferenceReservesOneDurableQuotaSlotBeforeProviderWork(t *testing.T) {
	t.Run("reserves once", func(t *testing.T) {
		mock := setupGatewayAttemptPolicyDB(t)
		taskID := uuid.Must(uuid.NewV4())
		callID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
		capability := inferencecapability.Scope{TaskID: taskID.String(), RunID: "run-quota", Operation: "delivery.plan", WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
		snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-quota", "delivery.plan", nil, 100, 4, []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}})
		expectGatewayLeasedTaskWithIdentity(mock, &taskID, nil, "delivery.plan", "run-quota", 100, workerID, "generalist", machineID)
		expectGatewayAttemptPolicyLookup(mock, snapshot)
		expectInferenceReceiptAbsent(mock, taskID, "run-quota", callID)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND run_id = \$2`).
			WithArgs(taskID.String(), "run-quota").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
		mock.ExpectQuery(`INSERT INTO "automation_inference_receipts".*RETURNING "id"`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(receiptID.String()))
		mock.ExpectCommit()

		scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{TaskID: taskID.String(), RunID: "run-quota", Operation: "delivery.plan", MaxCompletionTokens: 90, CallID: callID.String()}, capability)
		if err != nil || !configured || scope.CallID != callID || scope.ReceiptID != receiptID || scope.MaxCalls != snapshot.MaxInferenceCalls || scope.PolicyHash != snapshot.SnapshotHash {
			t.Fatalf("gateway did not reserve the call against its signed run quota: scope=%#v configured=%v err=%v", scope, configured, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("duplicate call id is never replayed", func(t *testing.T) {
		mock := setupGatewayAttemptPolicyDB(t)
		taskID := uuid.Must(uuid.NewV4())
		callID, receiptID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
		capability := inferencecapability.Scope{TaskID: taskID.String(), RunID: "run-duplicate", Operation: "delivery.plan", WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
		snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-duplicate", "delivery.plan", nil, 100, 4, []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}})
		expectGatewayLeasedTaskWithIdentity(mock, &taskID, nil, "delivery.plan", "run-duplicate", 100, workerID, "generalist", machineID)
		expectGatewayAttemptPolicyLookup(mock, snapshot)
		mock.ExpectQuery(`SELECT \* FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND run_id = \$2 AND call_id = \$3 LIMIT \$4`).
			WithArgs(taskID.String(), "run-duplicate", callID.String(), 1).
			WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "run_id", "call_id", "status"}).AddRow(receiptID.String(), taskID.String(), "run-duplicate", callID.String(), "ambiguous"))
		mock.ExpectRollback()

		scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{TaskID: taskID.String(), RunID: "run-duplicate", Operation: "delivery.plan", MaxCompletionTokens: 90, CallID: callID.String()}, capability)
		if err == nil || configured || scope.ReceiptID != uuid.Nil {
			t.Fatalf("reused call id must be rejected before provider work: scope=%#v configured=%v err=%v", scope, configured, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("quota counts ambiguous reservations", func(t *testing.T) {
		mock := setupGatewayAttemptPolicyDB(t)
		taskID := uuid.Must(uuid.NewV4())
		callID := uuid.Must(uuid.NewV4())
		workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
		capability := inferencecapability.Scope{TaskID: taskID.String(), RunID: "run-exhausted", Operation: "delivery.plan", WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
		snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-exhausted", "delivery.plan", nil, 100, 4, []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}})
		expectGatewayLeasedTaskWithIdentity(mock, &taskID, nil, "delivery.plan", "run-exhausted", 100, workerID, "generalist", machineID)
		expectGatewayAttemptPolicyLookup(mock, snapshot)
		expectInferenceReceiptAbsent(mock, taskID, "run-exhausted", callID)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND run_id = \$2`).
			WithArgs(taskID.String(), "run-exhausted").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(snapshot.MaxInferenceCalls)))
		mock.ExpectRollback()

		scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{TaskID: taskID.String(), RunID: "run-exhausted", Operation: "delivery.plan", MaxCompletionTokens: 90, CallID: callID.String()}, capability)
		if err == nil || configured || scope.ReceiptID != uuid.Nil {
			t.Fatalf("an ambiguous call must consume a quota slot: scope=%#v configured=%v err=%v", scope, configured, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("publish has zero inference quota", func(t *testing.T) {
		mock := setupGatewayAttemptPolicyDB(t)
		taskID := uuid.Must(uuid.NewV4())
		callID := uuid.Must(uuid.NewV4())
		workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
		capability := inferencecapability.Scope{TaskID: taskID.String(), RunID: "run-publish", Operation: "delivery.publish", WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
		snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-publish", "delivery.publish", nil, 100, 4, []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}})
		expectGatewayLeasedTaskWithIdentity(mock, &taskID, nil, "delivery.publish", "run-publish", 100, workerID, "generalist", machineID)
		expectGatewayAttemptPolicyLookup(mock, snapshot)
		expectInferenceReceiptAbsent(mock, taskID, "run-publish", callID)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND run_id = \$2`).
			WithArgs(taskID.String(), "run-publish").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
		mock.ExpectRollback()

		scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{TaskID: taskID.String(), RunID: "run-publish", Operation: "delivery.publish", MaxCompletionTokens: 90, CallID: callID.String()}, capability)
		if err == nil || configured || scope.ReceiptID != uuid.Nil || !strings.Contains(err.Error(), "quota") {
			t.Fatalf("publish must fail closed before provider work: scope=%#v configured=%v err=%v", scope, configured, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGatewayInferenceEnforcesOperationSpecificQuota(t *testing.T) {
	for _, scenario := range []struct {
		operation string
		quota     int
	}{
		{operation: "delivery.qa", quota: 2},
		{operation: "delivery.implementation", quota: 6},
	} {
		t.Run(scenario.operation, func(t *testing.T) {
			mock := setupGatewayAttemptPolicyDB(t)
			taskID, callID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
			workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
			capability := inferencecapability.Scope{TaskID: taskID.String(), RunID: "run-operation-quota", Operation: scenario.operation, WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
			snapshot := newGatewayAttemptPolicySnapshot(t, taskID, "run-operation-quota", scenario.operation, nil, 100, 4, []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}})
			if snapshot.MaxInferenceCalls != scenario.quota {
				t.Fatalf("frozen %s quota=%d, want %d", scenario.operation, snapshot.MaxInferenceCalls, scenario.quota)
			}
			expectGatewayLeasedTaskWithIdentity(mock, &taskID, nil, scenario.operation, "run-operation-quota", 100, workerID, "generalist", machineID)
			expectGatewayAttemptPolicyLookup(mock, snapshot)
			expectInferenceReceiptAbsent(mock, taskID, "run-operation-quota", callID)
			mock.ExpectQuery(`SELECT count\(\*\) FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND run_id = \$2`).
				WithArgs(taskID.String(), "run-operation-quota").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(scenario.quota)))
			mock.ExpectRollback()

			scope, configured, err := gatewayInferenceScopeForRequest(inferenceRequest{TaskID: taskID.String(), RunID: "run-operation-quota", Operation: scenario.operation, MaxCompletionTokens: 90, CallID: callID.String()}, capability)
			if err == nil || configured || scope.ReceiptID != uuid.Nil {
				t.Fatalf("%s must reject a call after its frozen quota is exhausted: scope=%#v configured=%v err=%v", scenario.operation, scope, configured, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectInferenceReceiptAbsent(mock sqlmock.Sqlmock, taskID uuid.UUID, runID string, callID uuid.UUID) {
	mock.ExpectQuery(`SELECT \* FROM "automation_inference_receipts" WHERE automation_task_id = \$1 AND run_id = \$2 AND call_id = \$3 LIMIT \$4`).
		WithArgs(taskID.String(), runID, callID.String(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id", "run_id", "call_id", "status"}))
}
