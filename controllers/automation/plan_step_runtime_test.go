package automation

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestPlanStepRuntimeCallbacksRequireEnrolledInstance(t *testing.T) {
	for _, test := range []struct {
		name    string
		method  string
		handler echo.HandlerFunc
		path    string
	}{
		{name: "claim", method: http.MethodPost, handler: ClaimDeliveryPlanStep, path: "/steps/claim"},
		{name: "renew", method: http.MethodPut, handler: RenewDeliveryPlanStepLease, path: "/steps/step"},
		{name: "transition", method: http.MethodPut, handler: TransitionDeliveryPlanStep, path: "/steps/step"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`))
			recorder := httptest.NewRecorder()
			ctx := echo.New().NewContext(request, recorder)
			if test.name != "claim" {
				ctx.SetParamNames("id")
				ctx.SetParamValues(uuid.Must(uuid.NewV4()).String())
			}
			if err := test.handler(ctx); err != nil {
				t.Fatalf("callback returned an unhandled error: %v", err)
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("missing authenticated instance should be rejected with 401, got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPlanStepRuntimeClaimRejectsInvalidPayloadWithoutEchoingBody(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/steps/claim", strings.NewReader(`{"task_id":"private-marker","unknown":"do-not-echo-this"}`))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	if err := ClaimDeliveryPlanStep(ctx); err != nil {
		t.Fatalf("invalid payload should be handled: %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid-payload status %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "do-not-echo-this") || strings.Contains(recorder.Body.String(), "private-marker") {
		t.Fatalf("callback response echoed private request data: %s", recorder.Body.String())
	}
}

func TestPlanStepRuntimeClaimRequiresDatabaseBeforeWork(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	taskID, runID, planID, workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	requestBody := `{"task_id":"` + taskID.String() + `","plan_id":"` + planID.String() + `","run_id":"` + runID.String() + `","worker_id":"` + workerID.String() + `","agent_key":"generalist","machine_id":"` + machineID.String() + `","lease_seconds":60}`
	request := httptest.NewRequest(http.MethodPost, "/steps/claim", strings.NewReader(requestBody))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: machineID.String()})
	if err := ClaimDeliveryPlanStep(ctx); err != nil {
		t.Fatalf("unavailable database response should be handled: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("claim without database must fail closed, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestPlanStepRuntimeClaimRejectsLegacyHeartbeatBeforeTaskClaim(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previousDB := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previousDB })

	taskID, planID, runID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workerID, machineID, instanceID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	body := `{"task_id":"` + taskID.String() + `","plan_id":"` + planID.String() + `","run_id":"` + runID.String() + `","worker_id":"` + workerID.String() + `","agent_key":"generalist","machine_id":"` + machineID.String() + `","lease_seconds":60}`
	request := httptest.NewRequest(http.MethodPost, "/steps/claim", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, recorder)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: instanceID, AgentKey: "generalist", MachineID: machineID.String()})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT protocols_json, draining, last_seen_at FROM "automation_agent_heartbeats" WHERE worker_id = \$1 AND agent_key = \$2 AND machine_id = \$3 AND agent_instance_id = \$4 AND last_seen_at >= \$5 AND last_seen_at <= \$6 LIMIT \$7`).
		WithArgs(workerID.String(), "generalist", machineID.String(), instanceID, sqlmock.AnyArg(), sqlmock.AnyArg(), 1).
		WillReturnRows(sqlmock.NewRows([]string{"protocols_json", "draining", "last_seen_at"}).AddRow(`[]`, false, time.Now().UTC()))
	mock.ExpectRollback()

	if err := ClaimDeliveryPlanStep(ctx); err != nil {
		t.Fatalf("legacy worker rejection should be handled: %v", err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("legacy runtime must not claim a plan step, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), workerID.String()) || strings.Contains(recorder.Body.String(), instanceID.String()) {
		t.Fatalf("claim rejection exposed runtime identifiers: %s", recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStepRuntimeTupleAndFenceValidation(t *testing.T) {
	taskID, runID, workerID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	if _, _, _, _, err := parsePlanStepRuntimeTuple(taskID.String(), runID.String(), workerID.String(), "generalist", "", 60); err != nil {
		t.Fatalf("valid callback tuple was rejected: %v", err)
	}
	for _, test := range []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "zero", input: "0"},
		{name: "negative", input: "-1"},
		{name: "nonnumeric", input: "not-a-fence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parsePlanStepFence(test.input); err == nil {
				t.Fatalf("invalid fence %q was accepted", test.input)
			}
		})
	}
	if fence, err := parsePlanStepFence("12"); err != nil || fence != 12 {
		t.Fatalf("valid fence was not parsed, fence=%d err=%v", fence, err)
	}
	if models.DeliveryPlanStepRunning != "running" {
		t.Fatal("test contract expects the canonical running state")
	}
}

func TestProjectPlanStepClaimResponseRequiresAndReturnsSafeStepProjection(t *testing.T) {
	leaseExpiresAt := time.Now().UTC().Add(time.Minute)
	step := &deliveryplansteps.StepDTO{
		ID: uuid.Must(uuid.NewV4()).String(), PlanID: uuid.Must(uuid.NewV4()).String(),
		PlanVersion: 2, StepKey: "implement", Order: 1, Title: "Implement change",
		Objective: "Apply the approved change", AcceptanceCriteria: []string{"tests pass"},
		DependsOn: []string{}, Status: models.DeliveryPlanStepRunning,
	}
	result := deliveryplansteps.ClaimResult{Available: true, Step: models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4())}, Fence: 7, LeaseExpiresAt: &leaseExpiresAt}

	response, err := projectPlanStepClaimResponse(result, step)
	if err != nil {
		t.Fatalf("valid assigned-step claim could not be projected: %v", err)
	}
	if !response.Available || response.Step != step || response.FencingToken != "7" || response.LeaseExpiresAt == nil || !response.LeaseExpiresAt.Equal(leaseExpiresAt) {
		t.Fatalf("claim response omitted the safe step, fence, or lease: %#v", response)
	}

	if _, err := projectPlanStepClaimResponse(result, nil); err == nil {
		t.Fatal("available claim without a step DTO must fail closed instead of returning an invalid response")
	}
}

func TestProjectPlanStepClaimResponseAllowsNoAvailableStep(t *testing.T) {
	response, err := projectPlanStepClaimResponse(deliveryplansteps.ClaimResult{Available: false}, nil)
	if err != nil {
		t.Fatalf("empty claim response should be valid: %v", err)
	}
	if response.Available || response.Step != nil || response.FencingToken != "" || response.LeaseExpiresAt != nil {
		t.Fatalf("no-work response should not expose claim data: %#v", response)
	}
}

func TestTerminalPlanStepReplayRejectsCrossExecutionBindings(t *testing.T) {
	planID, workItemID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	childTaskID, parentTaskID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	stepID, executionID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	workerID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	runID := uuid.Must(uuid.NewV4()).String()
	fence := int64(8)
	status := models.DeliveryPlanStepCompleted
	plan := models.DeliveryPlan{ID: planID, WorkItemID: workItemID, Version: 3}
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: planID, Status: status, AutomationTaskID: &childTaskID,
		RunID: runID, WorkerID: workerID, AgentKey: "generalist", MachineID: machineID, LeaseFence: fence,
	}
	childTask := models.AutomationTask{ID: childTaskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}
	parentTask := models.AutomationTask{ID: parentTaskID, Operation: "delivery.implementation", DeliveryWorkItemID: &workItemID}
	execution := models.DeliveryPlanExecution{ID: executionID, AutomationTaskID: parentTaskID, PlanID: planID, PlanVersion: plan.Version}
	assignment := models.DeliveryPlanStepAssignment{
		ID: uuid.Must(uuid.NewV4()), ExecutionID: executionID, DeliveryPlanStepID: stepID,
		ChildAutomationTaskID: childTaskID, Status: models.DeliveryPlanStepAssignmentCompleted,
	}
	identity := automationagent.AgentIdentity{WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
	if !terminalPlanStepReplayMatches(step, childTask, &parentTask, plan, &assignment, &execution, childTaskID, runID, identity, fence, status) {
		t.Fatal("a correctly bound terminal assignment should allow an idempotent replay")
	}

	cases := []struct {
		name            string
		assignment      models.DeliveryPlanStepAssignment
		execution       models.DeliveryPlanExecution
		parentTask      models.AutomationTask
		step            models.DeliveryPlanStep
		plan            models.DeliveryPlan
		childWorkItemID *uuid.UUID
	}{
		{name: "execution belongs to a different parent task", assignment: assignment, execution: models.DeliveryPlanExecution{ID: execution.ID, AutomationTaskID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, PlanVersion: plan.Version}, parentTask: parentTask, step: step, plan: plan},
		{name: "parent task belongs to a different work item", assignment: assignment, execution: execution, parentTask: models.AutomationTask{ID: parentTask.ID, Operation: parentTask.Operation, DeliveryWorkItemID: uuidPointer(uuid.Must(uuid.NewV4()))}, step: step, plan: plan},
		{name: "parent task is not a delivery implementation", assignment: assignment, execution: execution, parentTask: models.AutomationTask{ID: parentTask.ID, Operation: "automation.generic", DeliveryWorkItemID: parentTask.DeliveryWorkItemID}, step: step, plan: plan},
		{name: "child task belongs to a different work item", assignment: assignment, execution: execution, parentTask: parentTask, step: step, plan: plan, childWorkItemID: uuidPointer(uuid.Must(uuid.NewV4()))},
		{name: "execution freezes a different plan version", assignment: assignment, execution: models.DeliveryPlanExecution{ID: execution.ID, AutomationTaskID: parentTask.ID, PlanID: plan.ID, PlanVersion: plan.Version + 1}, parentTask: parentTask, step: step, plan: plan},
		{name: "step is from a different plan", assignment: assignment, execution: execution, parentTask: parentTask, step: models.DeliveryPlanStep{ID: step.ID, PlanID: uuid.Must(uuid.NewV4()), Status: step.Status, AutomationTaskID: step.AutomationTaskID, RunID: step.RunID, WorkerID: step.WorkerID, AgentKey: step.AgentKey, MachineID: step.MachineID, LeaseFence: step.LeaseFence}, plan: plan},
		{name: "assignment points at a different step", assignment: models.DeliveryPlanStepAssignment{ID: assignment.ID, ExecutionID: execution.ID, DeliveryPlanStepID: uuid.Must(uuid.NewV4()), ChildAutomationTaskID: childTask.ID, Status: assignment.Status}, execution: execution, parentTask: parentTask, step: step, plan: plan},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			changedChildTask := childTask
			if test.childWorkItemID != nil {
				changedChildTask.DeliveryWorkItemID = test.childWorkItemID
			}
			if terminalPlanStepReplayMatches(test.step, changedChildTask, &test.parentTask, test.plan, &test.assignment, &test.execution, childTaskID, runID, identity, fence, status) {
				t.Fatal("a cross-bound terminal assignment was accepted for replay")
			}
		})
	}
	if !terminalPlanStepReplayMatches(step, childTask, nil, plan, nil, nil, childTaskID, runID, identity, fence, status) {
		t.Fatal("a legacy sequential terminal step without an assignment should preserve its replay path")
	}
}

func TestLegacyPlanClaimIsSerializedAgainstActiveFanout(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	planID := uuid.Must(uuid.NewV4())
	workItemID, gateID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	query := `SELECT count\(\*\) FROM "delivery_plan_executions" WHERE plan_id = \$1 AND status IN \(\$2,\$3,\$4\)`
	statuses := []string{
		models.DeliveryPlanExecutionPending,
		models.DeliveryPlanExecutionDispatching,
		models.DeliveryPlanExecutionRunning,
	}
	for _, test := range []struct {
		name        string
		activeCount int64
		wantErr     error
	}{
		{name: "no active fanout", activeCount: 0},
		{name: "pending fanout reserves the plan", activeCount: 1, wantErr: errPlanStepActiveExecutionOwnsPlan},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock.ExpectQuery(`SELECT "id","work_item_id","version","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 AND work_item_id = \$2 ORDER BY "delivery_plans"\."id" LIMIT \$3 FOR SHARE`).
				WithArgs(planID, workItemID, 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "version", "status", "approved_gate_id"}).AddRow(planID, workItemID, 1, "approved", gateID))
			lockedPlan, err := lockDeliveryPlanForLegacyClaim(db, planID, workItemID)
			if err != nil {
				t.Fatalf("legacy path could not acquire its shared plan lock: %v", err)
			}
			if lockedPlan.Version != 1 {
				t.Fatalf("legacy path omitted the persisted plan version: got %d, want 1", lockedPlan.Version)
			}
			mock.ExpectQuery(query).
				WithArgs(planID, statuses[0], statuses[1], statuses[2]).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(test.activeCount))
			err = rejectLegacyPlanClaimDuringActiveExecution(db, planID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("legacy claim guard error = %v, want %v", err, test.wantErr)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func uuidPointer(id uuid.UUID) *uuid.UUID { return &id }
