package deliveryplansteps

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newClaimTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, mock
}

func claimFixture(now time.Time) (models.DeliveryPlan, models.AutomationTask, ClaimInput) {
	workItemID, planID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	taskID, runID, workerID, gateID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	machineID := uuid.Must(uuid.NewV4())
	expiresAt := now.Add(10 * time.Minute)
	task := models.AutomationTask{
		ID: taskID, DeliveryWorkItemID: &workItemID, Status: "running", RunID: runID.String(),
		WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String(), LeaseExpiresAt: &expiresAt,
	}
	plan := models.DeliveryPlan{ID: planID, WorkItemID: workItemID, Version: 3, Status: "approved", ApprovedGateID: &gateID}
	input := ClaimInput{
		PlanID: planID, AutomationTaskID: taskID, RunID: runID.String(), WorkerID: workerID.String(),
		AgentKey: "generalist", MachineID: machineID.String(), LeaseTTL: 2 * time.Minute, Now: now,
	}
	return plan, task, input
}

func expectClaimPlan(mock sqlmock.Sqlmock, plan models.DeliveryPlan, lock string) {
	query := `SELECT "id","work_item_id","version","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`
	if lock != "" {
		query += ` FOR ` + regexp.QuoteMeta(lock)
	}
	mock.ExpectQuery(query).WithArgs(plan.ID, 1).WillReturnRows(sqlmock.NewRows([]string{
		"id", "work_item_id", "version", "status", "approved_gate_id",
	}).AddRow(plan.ID, plan.WorkItemID, plan.Version, plan.Status, plan.ApprovedGateID))
}

func expectClaimTask(mock sqlmock.Sqlmock, task models.AutomationTask) {
	mock.ExpectQuery(`SELECT "id","delivery_work_item_id","status","run_id","worker_id","agent_key","machine_id","lease_expires_at" FROM "automation_tasks" WHERE id = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(task.ID, 1).WillReturnRows(sqlmock.NewRows([]string{
		"id", "delivery_work_item_id", "status", "run_id", "worker_id", "agent_key", "machine_id", "lease_expires_at",
	}).AddRow(task.ID, task.DeliveryWorkItemID.String(), task.Status, task.RunID, task.WorkerID, task.AgentKey, task.MachineID, task.LeaseExpiresAt))
}

func emptyClaimStepRows() *sqlmock.Rows { return sqlmock.NewRows([]string{"id"}) }

func planStepClaimRows(step models.DeliveryPlanStep) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "plan_id", "step_key", "idempotency_key", "display_order", "title", "objective", "acceptance_criteria_json", "status",
		"agent_key", "worker_id", "machine_id", "automation_task_id", "automation_execution_id", "run_id", "lease_fence", "lease_expires_at",
		"created_by", "started_at", "completed_at", "created_at", "updated_at",
	}).AddRow(
		step.ID, step.PlanID, step.StepKey, step.IdempotencyKey, step.DisplayOrder, step.Title, step.Objective, step.AcceptanceCriteriaJSON, step.Status,
		step.AgentKey, step.WorkerID, step.MachineID, step.AutomationTaskID, step.AutomationExecutionID, step.RunID, step.LeaseFence, step.LeaseExpiresAt,
		step.CreatedBy, step.StartedAt, step.CompletedAt, time.Now().UTC(), time.Now().UTC(),
	)
}

func expectStepLookup(mock sqlmock.Sqlmock, step models.DeliveryPlanStep) {
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE id = \$1 ORDER BY "delivery_plan_steps"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(step.ID, 1).WillReturnRows(planStepClaimRows(step))
}

func expectPlanStepEventInsert(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`INSERT INTO "delivery_plan_step_events"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(uuid.Must(uuid.NewV4())))
}

func TestClaimInputRejectsMissingAndUnsafeIdentityBeforeDatabaseAccess(t *testing.T) {
	db, mock := newClaimTestDB(t)
	now := time.Now().UTC()
	_, _, valid := claimFixture(now)

	_, err := ClaimNextReadyInTransaction(db, ClaimInput{})
	if !errors.Is(err, ErrStepInputInvalid) {
		t.Fatalf("empty claim error = %v, want ErrStepInputInvalid", err)
	}
	invalid := valid
	invalid.WorkerID = "worker-hostname"
	if _, err := ClaimNextReadyInTransaction(db, invalid); !errors.Is(err, ErrStepInputInvalid) {
		t.Fatalf("non-opaque worker identity error = %v, want ErrStepInputInvalid", err)
	}
	invalid = valid
	invalid.LeaseTTL = 0
	if _, err := ClaimNextReadyInTransaction(db, invalid); !errors.Is(err, ErrStepInputInvalid) {
		t.Fatalf("invalid lease TTL error = %v, want ErrStepInputInvalid", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimRequiresApprovedPlanAndMatchingLiveTaskLease(t *testing.T) {
	now := time.Now().UTC()
	plan, task, input := claimFixture(now)
	db, mock := newClaimTestDB(t)
	unapproved := plan
	unapproved.Status = "proposed"
	expectClaimPlan(mock, unapproved, "SHARE")
	if _, err := ClaimNextReadyInTransaction(db, input); !errors.Is(err, ErrStepInputInvalid) {
		t.Fatalf("unapproved plan error = %v, want ErrStepInputInvalid", err)
	}

	badTask := task
	badTask.RunID = uuid.Must(uuid.NewV4()).String()
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, badTask)
	if _, err := ClaimNextReadyInTransaction(db, input); !errors.Is(err, ErrStepLeaseConflict) {
		t.Fatalf("mismatched task run error = %v, want ErrStepLeaseConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimRechecksTaskLeaseAfterWaitingForTaskRowLock(t *testing.T) {
	startedAt := time.Now().UTC()
	plan, task, input := claimFixture(startedAt)
	expiresAt := startedAt.Add(100 * time.Millisecond)
	task.LeaseExpiresAt = &expiresAt
	input.Now = time.Time{} // Runtime path: use the clock after acquiring locks.
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	mock.ExpectQuery(`SELECT "id","delivery_work_item_id","status","run_id","worker_id","agent_key","machine_id","lease_expires_at" FROM "automation_tasks" WHERE id = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(task.ID, 1).
		WillDelayFor(250 * time.Millisecond).
		WillReturnRows(sqlmock.NewRows([]string{"id", "delivery_work_item_id", "status", "run_id", "worker_id", "agent_key", "machine_id", "lease_expires_at"}).
			AddRow(task.ID, task.DeliveryWorkItemID, task.Status, task.RunID, task.WorkerID, task.AgentKey, task.MachineID, task.LeaseExpiresAt))

	if _, err := ClaimNextReadyInTransaction(db, input); !errors.Is(err, ErrStepLeaseConflict) {
		t.Fatalf("claim after the task lease expires while waiting for its row lock = %v, want ErrStepLeaseConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimReturnsUnavailableWhenNoStepHasCompletedDependencies(t *testing.T) {
	now := time.Now().UTC()
	plan, task, input := claimFixture(now)
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*NOT EXISTS .*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())

	result, err := ClaimNextReadyInTransaction(db, input)
	if err != nil || result.Available {
		t.Fatalf("no ready step = %#v, %v; want available=false with no error", result, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != `{"available":false}` {
		t.Fatalf("unavailable claim JSON = %s, %v; want only available=false", encoded, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimAssignsFirstReadyStepWithMonotonicFenceAndEvents(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	input.Now = now
	input.LeaseTTL = 2 * time.Minute
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	stepID := uuid.Must(uuid.NewV4())
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: plan.ID, StepKey: "prepare", IdempotencyKey: "step:prepare", DisplayOrder: 0,
		Title: "Prepare", Objective: "Review scope", AcceptanceCriteriaJSON: `["Reviewed"]`, Status: models.DeliveryPlanStepPlanned,
	}
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*NOT EXISTS .*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(step))
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)

	result, err := ClaimNextReadyInTransaction(db, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Step.ID != stepID || result.Step.Status != models.DeliveryPlanStepRunning || result.Fence != 1 || result.Step.AutomationTaskID == nil || *result.Step.AutomationTaskID != task.ID {
		t.Fatalf("unexpected claimed step: %#v", result)
	}
	wantExpiry := now.Add(input.LeaseTTL)
	if result.LeaseExpiresAt == nil || !result.LeaseExpiresAt.Equal(wantExpiry) {
		t.Fatalf("step lease expiry = %v, want %v", result.LeaseExpiresAt, wantExpiry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimSpecificReadyStepDoesNotFallThroughToAnotherReadyStep(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	targetStepID := uuid.Must(uuid.NewV4())
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*NOT EXISTS .*AND id = \$[0-9]+.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())

	result, err := ClaimSpecificReadyInTransaction(db, input, targetStepID)
	if err != nil || result.Available {
		t.Fatalf("unavailable assigned step must not fall through: %#v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimSpecificReadyStepOnlyReturnsItsAssignedStep(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	targetStepID := uuid.Must(uuid.NewV4())
	step := models.DeliveryPlanStep{
		ID: targetStepID, PlanID: plan.ID, StepKey: "target", IdempotencyKey: "step:target", DisplayOrder: 5,
		Title: "Target", Objective: "Execute only this assignment", AcceptanceCriteriaJSON: `["verified"]`, Status: models.DeliveryPlanStepReady,
	}
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*NOT EXISTS .*AND id = \$[0-9]+.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(step))
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)

	result, err := ClaimSpecificReadyInTransaction(db, input, targetStepID)
	if err != nil || !result.Available || result.Step.ID != targetStepID {
		t.Fatalf("target claim = %#v, %v; want only %s", result, err, targetStepID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimAllowsDependentStepOnceAllParentsAreCompleted(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	dependent := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "dependent", IdempotencyKey: "step:dependent", DisplayOrder: 2,
		Title: "Dependent", Objective: "Consume verified parent patches", Status: models.DeliveryPlanStepReady,
	}
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*delivery_plan_step_dependencies AS edge.*dependency.status <> \$[0-9]+.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(dependent))
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)

	result, err := ClaimNextReadyInTransaction(db, input)
	if err != nil || !result.Available || result.Step.ID != dependent.ID {
		t.Fatalf("dependent claim after completed parents = %#v, %v; want %s", result, err, dependent.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimKeepsDependentUnavailableUntilEveryParentCompletes(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*delivery_plan_step_dependencies AS edge.*dependency.status <> \$[0-9]+.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())

	result, err := ClaimNextReadyInTransaction(db, input)
	if err != nil || result.Available {
		t.Fatalf("dependent step with an incomplete parent must remain unavailable: %#v, %v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimSpecificReadyStepRejectsConflictingActiveStepForSameTask(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	targetStepID, activeStepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expiresAt := now.Add(time.Minute)
	active := models.DeliveryPlanStep{
		ID: activeStepID, PlanID: plan.ID, StepKey: "active", IdempotencyKey: "step:active", DisplayOrder: 0,
		Status: models.DeliveryPlanStepRunning, AutomationTaskID: &task.ID, RunID: task.RunID, WorkerID: task.WorkerID,
		AgentKey: task.AgentKey, MachineID: task.MachineID, LeaseFence: 2, LeaseExpiresAt: &expiresAt,
	}
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(active))

	if _, err := ClaimSpecificReadyInTransaction(db, input, targetStepID); !errors.Is(err, ErrStepLeaseConflict) {
		t.Fatalf("claim for another step while one is active = %v; want ErrStepLeaseConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimReclaimsOnlyExpiredRunningStepAndAdvancesFence(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, input := claimFixture(now)
	db, mock := newClaimTestDB(t)
	expectClaimPlan(mock, plan, "SHARE")
	expectClaimTask(mock, task)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND automation_task_id = \$2.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(emptyClaimStepRows())
	stepID := uuid.Must(uuid.NewV4())
	oldTaskID, oldWorkerID, oldRunID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expiredAt := now.Add(-time.Second)
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: plan.ID, StepKey: "build", IdempotencyKey: "step:build", DisplayOrder: 1,
		Title: "Build", Objective: "Implement the change", AcceptanceCriteriaJSON: `["Works"]`, Status: models.DeliveryPlanStepRunning,
		AutomationTaskID: &oldTaskID, RunID: oldRunID.String(), WorkerID: oldWorkerID.String(), AgentKey: "generalist",
		LeaseFence: 4, LeaseExpiresAt: &expiredAt,
	}
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 AND .*\(agent_key = '' OR agent_key = \$[0-9]+\).*NOT EXISTS .*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(step))
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)

	result, err := ClaimNextReadyInTransaction(db, input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Step.ID != stepID || result.Fence != 5 || result.Step.RunID != task.RunID || result.Step.WorkerID != task.WorkerID {
		t.Fatalf("expired lease should be reclaimed by the current task with fence 5: %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewRejectsStaleFenceAndRequiresCurrentTaskAndStepLeases(t *testing.T) {
	now := time.Now().UTC()
	_, task, claim := claimFixture(now)
	stepID := uuid.Must(uuid.NewV4())
	stepTaskID := task.ID
	stepExpiry := now.Add(time.Minute)
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: claim.PlanID, Status: models.DeliveryPlanStepRunning, AutomationTaskID: &stepTaskID,
		RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
		LeaseFence: 7, LeaseExpiresAt: &stepExpiry,
	}
	db, mock := newClaimTestDB(t)
	expectClaimTask(mock, task)
	expectStepLookup(mock, step)
	input := LeaseInput{
		StepID: stepID, AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey,
		MachineID: task.MachineID, Fence: 6, LeaseTTL: time.Minute, Now: now,
	}
	if err := RenewStepLeaseInTransaction(db, input); !errors.Is(err, ErrStepLeaseConflict) {
		t.Fatalf("stale fence error = %v, want ErrStepLeaseConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewExtendsLeaseWithinParentTaskLeaseAndAppendsEvent(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	plan, task, claim := claimFixture(now)
	stepID, taskID := uuid.Must(uuid.NewV4()), task.ID
	stepExpiry := now.Add(time.Minute)
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: plan.ID, Status: models.DeliveryPlanStepRunning, AutomationTaskID: &taskID,
		RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
		LeaseFence: 9, LeaseExpiresAt: &stepExpiry,
	}
	db, mock := newClaimTestDB(t)
	expectClaimTask(mock, task)
	expectStepLookup(mock, step)
	mock.ExpectQuery(`SELECT "id","work_item_id","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(plan.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "status", "approved_gate_id"}).AddRow(plan.ID, plan.WorkItemID, "approved", plan.ApprovedGateID))
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)

	input := LeaseInput{
		StepID: stepID, AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey,
		MachineID: task.MachineID, Fence: 9, LeaseTTL: 3 * time.Minute, Now: claim.Now,
	}
	if err := RenewStepLeaseInTransaction(db, input); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRenewRechecksTaskAndStepLeasesAfterWaitingForStepLock(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		expireTask bool
	}{
		{name: "task lease expires during wait", expireTask: true},
		{name: "step lease expires during wait"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			startedAt := time.Now().UTC()
			plan, task, _ := claimFixture(startedAt)
			taskExpiresAt := startedAt.Add(10 * time.Minute)
			stepExpiresAt := startedAt.Add(10 * time.Minute)
			if scenario.expireTask {
				taskExpiresAt = startedAt.Add(100 * time.Millisecond)
			} else {
				stepExpiresAt = startedAt.Add(100 * time.Millisecond)
			}
			task.LeaseExpiresAt = &taskExpiresAt
			stepID, taskID := uuid.Must(uuid.NewV4()), task.ID
			step := models.DeliveryPlanStep{
				ID: stepID, PlanID: plan.ID, Status: models.DeliveryPlanStepRunning, AutomationTaskID: &taskID,
				RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
				LeaseFence: 7, LeaseExpiresAt: &stepExpiresAt,
			}
			db, mock := newClaimTestDB(t)
			expectClaimTask(mock, task)
			mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE id = \$1 ORDER BY "delivery_plan_steps"\."id" LIMIT \$2 FOR UPDATE`).
				WithArgs(step.ID, 1).
				WillDelayFor(250 * time.Millisecond).
				WillReturnRows(planStepClaimRows(step))
			input := LeaseInput{
				StepID: stepID, AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: task.WorkerID,
				AgentKey: task.AgentKey, MachineID: task.MachineID, Fence: 7, LeaseTTL: time.Minute,
				Now: time.Time{},
			}
			if err := RenewStepLeaseInTransaction(db, input); !errors.Is(err, ErrStepLeaseConflict) {
				t.Fatalf("renew after lease expiry during step-lock wait = %v, want ErrStepLeaseConflict", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransitionRejectsExpiredAutomationTaskOrStepLease(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 10, 0, 0, time.UTC)
	plan, task, _ := claimFixture(now)
	stepID := uuid.Must(uuid.NewV4())
	taskID := task.ID
	for _, scenario := range []struct {
		name       string
		expireTask bool
		expireStep bool
	}{
		{name: "expired automation task lease", expireTask: true},
		{name: "expired step lease", expireStep: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, mock := newClaimTestDB(t)
			caseTask := task
			stepExpiry := now.Add(time.Minute)
			if scenario.expireTask {
				expiredTaskLease := now.Add(-time.Second)
				caseTask.LeaseExpiresAt = &expiredTaskLease
			}
			if scenario.expireStep {
				stepExpiry = now.Add(-time.Second)
			}
			step := models.DeliveryPlanStep{
				ID: stepID, PlanID: plan.ID, Status: models.DeliveryPlanStepRunning, AutomationTaskID: &taskID,
				RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
				LeaseFence: 2, LeaseExpiresAt: &stepExpiry,
			}
			expectClaimTask(mock, caseTask)
			if !scenario.expireTask {
				expectStepLookup(mock, step)
			}
			input := TransitionInput{
				StepID: stepID, AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: task.WorkerID,
				AgentKey: task.AgentKey, MachineID: task.MachineID, Fence: 2, Status: models.DeliveryPlanStepCompleted, Now: now,
			}
			if err := TransitionStepInTransaction(db, input); !errors.Is(err, ErrStepLeaseConflict) {
				t.Fatalf("expired lease error = %v, want ErrStepLeaseConflict", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransitionAppendsFixedSafeEventAndRepeatingStatusIsIdempotent(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 10, 0, 0, time.UTC)
	plan, task, _ := claimFixture(now)
	stepID := uuid.Must(uuid.NewV4())
	taskID := task.ID
	stepExpiry := now.Add(time.Minute)
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: plan.ID, StepKey: "prepare", Status: models.DeliveryPlanStepRunning,
		AutomationTaskID: &taskID, RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey, MachineID: task.MachineID,
		LeaseFence: 3, LeaseExpiresAt: &stepExpiry,
	}
	db, mock := newClaimTestDB(t)
	expectClaimTask(mock, task)
	expectStepLookup(mock, step)
	mock.ExpectQuery(`SELECT "id","work_item_id","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(plan.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "status", "approved_gate_id"}).AddRow(plan.ID, plan.WorkItemID, "approved", plan.ApprovedGateID))
	mock.ExpectExec(`UPDATE "delivery_plan_steps" SET .* WHERE id = \$[0-9]+`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectPlanStepEventInsert(mock)

	transition := TransitionInput{
		StepID: stepID, AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: task.WorkerID,
		AgentKey: task.AgentKey, MachineID: task.MachineID, Fence: 3, Status: models.DeliveryPlanStepCompleted, Now: now,
	}
	if err := TransitionStepInTransaction(db, transition); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	completedStep := step
	completedStep.Status = models.DeliveryPlanStepCompleted
	expectClaimTask(mock, task)
	expectStepLookup(mock, completedStep)
	mock.ExpectQuery(`SELECT "id","work_item_id","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 ORDER BY "delivery_plans"\."id" LIMIT \$2`).
		WithArgs(plan.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "status", "approved_gate_id"}).AddRow(plan.ID, plan.WorkItemID, "approved", plan.ApprovedGateID))
	transition.Status = models.DeliveryPlanStepCompleted
	if err := TransitionStepInTransaction(db, transition); err != nil {
		t.Fatalf("repeating completed status should be idempotent while the lease remains live: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(models.DeliveryPlanStepEvent{
		Summary: "Step completed", RunID: task.RunID, WorkerID: task.WorkerID, AgentKey: task.AgentKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "secret", "token", task.RunID, task.WorkerID} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("event model unexpectedly serializes internal field %q: %s", forbidden, string(encoded))
		}
	}
}

func TestTransitionRejectsInvalidStatusWithInputSentinel(t *testing.T) {
	_, task, claim := claimFixture(time.Now().UTC())
	db, mock := newClaimTestDB(t)
	input := TransitionInput{
		StepID: uuid.Must(uuid.NewV4()), AutomationTaskID: task.ID, RunID: task.RunID, WorkerID: task.WorkerID,
		AgentKey: task.AgentKey, MachineID: task.MachineID, Fence: 1, Status: "secret-prompt", Now: claim.Now,
	}
	if err := TransitionStepInTransaction(db, input); !errors.Is(err, ErrStepInputInvalid) {
		t.Fatalf("invalid status error = %v, want ErrStepInputInvalid", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
