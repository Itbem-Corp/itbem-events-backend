package deliveryplansteps

import (
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

func newOrchestrationTestDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
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

func orchestrationFixture() (models.DeliveryPlan, []models.DeliveryPlanStep, []models.DeliveryPlanStepDependency, models.AutomationTask, time.Time) {
	plan, steps, dependencies := planHashFixture()
	workItemID := uuid.Must(uuid.NewV4())
	plan.WorkItemID = workItemID
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	parent := models.AutomationTask{ID: uuid.Must(uuid.NewV4()), DeliveryWorkItemID: &workItemID}
	steps[0].Status = models.DeliveryPlanStepCompleted
	steps[1].Status = models.DeliveryPlanStepPlanned
	return plan, steps, dependencies, parent, now
}

func expectOrchestrationParentLock(mock sqlmock.Sqlmock, parent models.AutomationTask) {
	mock.ExpectQuery(`SELECT "id","delivery_work_item_id" FROM "automation_tasks" WHERE id = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(parent.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "delivery_work_item_id"}).AddRow(parent.ID, parent.DeliveryWorkItemID))
}

func expectOrchestrationParentMissing(mock sqlmock.Sqlmock, parent models.AutomationTask) {
	mock.ExpectQuery(`SELECT "id","delivery_work_item_id" FROM "automation_tasks" WHERE id = \$1 ORDER BY "automation_tasks"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(parent.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "delivery_work_item_id"}))
}

func expectExecutionByParent(mock sqlmock.Sqlmock, parentID uuid.UUID, execution models.DeliveryPlanExecution) {
	mock.ExpectQuery(`SELECT "id","automation_task_id","idempotency_key","plan_id","plan_version","approved_gate_id","plan_hash","max_concurrency","status","dispatched_at","started_at","completed_at","created_at","updated_at" FROM "delivery_plan_executions" WHERE automation_task_id = \$1 LIMIT \$2 FOR UPDATE`).
		WithArgs(parentID, 1).WillReturnRows(executionRows(execution))
}

func expectExecutionByParentMissing(mock sqlmock.Sqlmock, parentID uuid.UUID) {
	mock.ExpectQuery(`SELECT "id","automation_task_id","idempotency_key","plan_id","plan_version","approved_gate_id","plan_hash","max_concurrency","status","dispatched_at","started_at","completed_at","created_at","updated_at" FROM "delivery_plan_executions" WHERE automation_task_id = \$1 LIMIT \$2 FOR UPDATE`).
		WithArgs(parentID, 1).WillReturnRows(executionRowsColumns())
}

func executionRows(execution models.DeliveryPlanExecution) *sqlmock.Rows {
	return executionRowsColumns().AddRow(execution.ID, execution.AutomationTaskID, execution.IdempotencyKey,
		execution.PlanID, execution.PlanVersion, execution.ApprovedGateID, execution.PlanHash,
		execution.MaxConcurrency, execution.Status, execution.DispatchedAt, execution.StartedAt,
		execution.CompletedAt, execution.CreatedAt, execution.UpdatedAt)
}

func executionRowsColumns() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "automation_task_id", "idempotency_key", "plan_id", "plan_version", "approved_gate_id", "plan_hash", "max_concurrency", "status", "dispatched_at", "started_at", "completed_at", "created_at", "updated_at"})
}

func expectPlanRows(mock sqlmock.Sqlmock, plan models.DeliveryPlan) {
	mock.ExpectQuery(`SELECT "id","work_item_id","version","status","approved_gate_id" FROM "delivery_plans" WHERE id = \$1 LIMIT \$2 FOR UPDATE`).
		WithArgs(plan.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "version", "status", "approved_gate_id"}).
		AddRow(plan.ID, plan.WorkItemID, plan.Version, plan.Status, plan.ApprovedGateID))
}

func expectGateRows(mock sqlmock.Sqlmock, gateID uuid.UUID, workItemID uuid.UUID) {
	mock.ExpectQuery(`SELECT "id","work_item_id","kind","decision" FROM "delivery_gates" WHERE id = \$1 LIMIT \$2 FOR UPDATE`).
		WithArgs(gateID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id", "kind", "decision"}).
		AddRow(gateID, workItemID, "plan", "approved"))
}

func expectPlanGraphRows(mock sqlmock.Sqlmock, plan models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency) {
	stepRows := sqlmock.NewRows([]string{
		"id", "plan_id", "step_key", "idempotency_key", "role", "display_order", "title", "objective", "acceptance_criteria_json", "status",
		"agent_key", "worker_id", "machine_id", "automation_task_id", "automation_execution_id", "run_id", "lease_fence", "lease_expires_at",
		"created_by", "started_at", "completed_at", "created_at", "updated_at",
	})
	for _, step := range steps {
		stepRows.AddRow(step.ID, step.PlanID, step.StepKey, step.IdempotencyKey, step.Role, step.DisplayOrder, step.Title, step.Objective, step.AcceptanceCriteriaJSON,
			step.Status, step.AgentKey, step.WorkerID, step.MachineID, step.AutomationTaskID, step.AutomationExecutionID, step.RunID, step.LeaseFence,
			step.LeaseExpiresAt, step.CreatedBy, step.StartedAt, step.CompletedAt, time.Time{}, time.Time{})
	}
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_steps" WHERE plan_id = \$1 ORDER BY display_order ASC, id ASC FOR UPDATE`).
		WithArgs(plan.ID).WillReturnRows(stepRows)

	dependencyRows := sqlmock.NewRows([]string{"id", "plan_id", "step_id", "depends_on_step_id", "created_at"})
	for _, dependency := range dependencies {
		dependencyRows.AddRow(uuid.Must(uuid.NewV4()), dependency.PlanID, dependency.StepID, dependency.DependsOnStepID, time.Time{})
	}
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_dependencies" WHERE plan_id = \$1 ORDER BY step_id ASC, depends_on_step_id ASC FOR UPDATE`).
		WithArgs(plan.ID).WillReturnRows(dependencyRows)
}

func expectPlanSnapshot(mock sqlmock.Sqlmock, plan models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency) {
	expectPlanRows(mock, plan)
	expectGateRows(mock, *plan.ApprovedGateID, plan.WorkItemID)
	expectPlanGraphRows(mock, plan, steps, dependencies)
}

func expectLiveLegacyStepLeaseCheck(mock sqlmock.Sqlmock, planID uuid.UUID, count int64) {
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM "delivery_plan_steps" WHERE \(plan_id = \$1 AND status = \$2 AND lease_expires_at > \$3\) AND NOT EXISTS \(.*delivery_plan_step_assignments AS assignment.*assignment\.delivery_plan_step_id = delivery_plan_steps\.id.*\)`).
		WithArgs(planID, models.DeliveryPlanStepRunning, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func expectPlanWorkItem(mock sqlmock.Sqlmock, planID, workItemID uuid.UUID) {
	mock.ExpectQuery(`SELECT "id","work_item_id" FROM "delivery_plans" WHERE id = \$1 LIMIT \$2`).
		WithArgs(planID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "work_item_id"}).AddRow(planID, workItemID))
}

func expectTaskOrganizationScope(mock sqlmock.Sqlmock, taskID, workItemID, projectID, clientID uuid.UUID) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT task.id AS task_id, delivery_work_items.id AS work_item_id, delivery_work_items.project_id AS project_id, delivery_projects.client_id AS client_id FROM automation_tasks AS task JOIN delivery_work_items ON delivery_work_items.id = task.delivery_work_item_id JOIN delivery_projects ON delivery_projects.id = delivery_work_items.project_id WHERE task.id = $1 LIMIT $2")).
		WithArgs(taskID, 1).WillReturnRows(sqlmock.NewRows([]string{"task_id", "work_item_id", "project_id", "client_id"}).AddRow(taskID, workItemID, projectID, clientID))
}

func expectAssignmentRows(mock sqlmock.Sqlmock, assignments ...models.DeliveryPlanStepAssignment) {
	rows := sqlmock.NewRows([]string{"id", "execution_id", "delivery_plan_step_id", "child_automation_task_id", "target_machine_id", "target_agent_key", "status", "queued_at", "dispatched_at", "started_at", "completed_at", "created_at", "updated_at"})
	for _, assignment := range assignments {
		rows.AddRow(assignment.ID, assignment.ExecutionID, assignment.DeliveryPlanStepID, assignment.ChildAutomationTaskID,
			assignment.TargetMachineID, assignment.TargetAgentKey, assignment.Status, assignment.QueuedAt, assignment.DispatchedAt, assignment.StartedAt, assignment.CompletedAt, assignment.CreatedAt, assignment.UpdatedAt)
	}
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_step_assignments" WHERE execution_id = \$1 AND delivery_plan_step_id IN \(.*\) ORDER BY delivery_plan_step_id ASC, id ASC`).
		WillReturnRows(rows)
}

func expectReserveExecutionIdentityAndLocks(mock sqlmock.Sqlmock, execution models.DeliveryPlanExecution, parent models.AutomationTask) {
	mock.ExpectQuery(`SELECT "id","automation_task_id" FROM "delivery_plan_executions" WHERE id = \$1 LIMIT \$2`).
		WithArgs(execution.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id"}).AddRow(execution.ID, parent.ID))
	expectOrchestrationParentLock(mock, parent)
	mock.ExpectQuery(`SELECT "id","automation_task_id","idempotency_key","plan_id","plan_version","approved_gate_id","plan_hash","max_concurrency","status","dispatched_at","started_at","completed_at","created_at","updated_at" FROM "delivery_plan_executions" WHERE id = \$1 ORDER BY "delivery_plan_executions"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(execution.ID, 1).WillReturnRows(executionRows(execution))
}

func orchestrationExecution(plan models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency, parent models.AutomationTask, maxConcurrency int, now time.Time) models.DeliveryPlanExecution {
	hash, _ := ApprovedPlanContentHash(plan, steps, dependencies)
	return models.DeliveryPlanExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parent.ID, IdempotencyKey: "dispatch-" + parent.ID.String(),
		PlanID: plan.ID, PlanVersion: plan.Version, ApprovedGateID: *plan.ApprovedGateID,
		MaxConcurrency: maxConcurrency, Status: models.DeliveryPlanExecutionPending, PlanHash: hash,
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestCreateExecutionIsIdempotentForParentTask(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	execution := orchestrationExecution(plan, steps, dependencies, parent, 3, now)

	expectOrchestrationParentLock(mock, parent)
	expectExecutionByParentMissing(mock, parent.ID)
	expectPlanSnapshot(mock, plan, steps, dependencies)
	expectLiveLegacyStepLeaseCheck(mock, plan.ID, 0)
	mock.ExpectQuery(`INSERT INTO "delivery_plan_executions"`).
		WithArgs(parent.ID, execution.IdempotencyKey, plan.ID, plan.Version, *plan.ApprovedGateID,
			execution.PlanHash, execution.MaxConcurrency, models.DeliveryPlanExecutionPending,
			nil, nil, nil, now, now, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(execution.ID))

	created, inserted, err := CreateExecutionInTransaction(db, parent.ID, plan.ID, execution.MaxConcurrency, execution.IdempotencyKey, now)
	if err != nil || !inserted || created.ID != execution.ID || created.PlanHash != execution.PlanHash {
		t.Fatalf("first creation = (%+v, %v, %v), want inserted execution", created, inserted, err)
	}

	expectOrchestrationParentLock(mock, parent)
	expectExecutionByParent(mock, parent.ID, execution)
	expectPlanWorkItem(mock, plan.ID, plan.WorkItemID)
	replayed, inserted, err := CreateExecutionInTransaction(db, parent.ID, plan.ID, execution.MaxConcurrency, execution.IdempotencyKey, now.Add(time.Minute))
	if err != nil || inserted || replayed.ID != execution.ID || replayed.PlanHash != execution.PlanHash {
		t.Fatalf("idempotent replay = (%+v, %v, %v), want existing execution", replayed, inserted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateExecutionRejectsLiveLegacyStepLeaseBeforeFanout(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	legacyTaskID := uuid.Must(uuid.NewV4())
	legacyLeaseExpiry := time.Now().UTC().Add(time.Minute)
	steps[1].Status = models.DeliveryPlanStepRunning
	steps[1].AutomationTaskID = &legacyTaskID
	steps[1].LeaseExpiresAt = &legacyLeaseExpiry
	execution := orchestrationExecution(plan, steps, dependencies, parent, 2, now)

	expectOrchestrationParentLock(mock, parent)
	expectExecutionByParentMissing(mock, parent.ID)
	expectPlanSnapshot(mock, plan, steps, dependencies)
	expectLiveLegacyStepLeaseCheck(mock, plan.ID, 1)

	if _, created, err := CreateExecutionInTransaction(db, parent.ID, plan.ID, execution.MaxConcurrency, execution.IdempotencyKey, now); !errors.Is(err, ErrPlanExecutionConflict) || created {
		t.Fatalf("fan-out with a live unassigned legacy lease = (created %t, err %v), want conflict", created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateExecutionRejectsParentPlanWorkItemMismatch(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, _, _, parent, now := orchestrationFixture()
	otherWorkItemID := uuid.Must(uuid.NewV4())
	plan.WorkItemID = otherWorkItemID
	expectOrchestrationParentLock(mock, parent)
	expectExecutionByParentMissing(mock, parent.ID)
	expectPlanRows(mock, plan)

	if _, _, err := CreateExecutionInTransaction(db, parent.ID, plan.ID, 1, "request-1", now); !errors.Is(err, ErrPlanExecutionParentMismatch) {
		t.Fatalf("mismatched parent work item error = %v, want ErrPlanExecutionParentMismatch", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateExecutionRejectsDifferentIdempotencyKeyForParent(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	execution := orchestrationExecution(plan, steps, dependencies, parent, 2, now)
	expectOrchestrationParentLock(mock, parent)
	expectExecutionByParent(mock, parent.ID, execution)

	if _, created, err := CreateExecutionInTransaction(db, parent.ID, plan.ID, execution.MaxConcurrency, "another-key", now); !errors.Is(err, ErrPlanExecutionConflict) || created {
		t.Fatalf("different idempotency key = (created %v, err %v), want conflict", created, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectReserveSnapshotPreamble(mock sqlmock.Sqlmock, execution models.DeliveryPlanExecution, plan models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency, parent models.AutomationTask, childTaskIDs map[uuid.UUID]uuid.UUID) {
	expectReserveExecutionIdentityAndLocks(mock, execution, parent)
	parentProjectID, parentClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectTaskOrganizationScope(mock, parent.ID, *parent.DeliveryWorkItemID, parentProjectID, parentClientID)
	expectPlanSnapshot(mock, plan, steps, dependencies)
	stepIDs, err := normalizeStepTaskMapping(childTaskIDs)
	if err != nil {
		panic(err)
	}
	for _, stepID := range stepIDs {
		childTaskID := childTaskIDs[stepID]
		expectTaskOrganizationScope(mock, childTaskID, *parent.DeliveryWorkItemID, parentProjectID, parentClientID)
	}
}

func TestReserveReadyAssignmentsHonorsDAGAndIsIdempotent(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	execution := orchestrationExecution(plan, steps, dependencies, parent, 2, now)
	childTaskID := uuid.Must(uuid.NewV4())
	childTaskIDs := map[uuid.UUID]uuid.UUID{steps[1].ID: childTaskID}
	assignmentID := uuid.Must(uuid.NewV4())
	assignment := models.DeliveryPlanStepAssignment{
		ID: assignmentID, ExecutionID: execution.ID, DeliveryPlanStepID: steps[1].ID,
		ChildAutomationTaskID: childTaskID, TargetMachineID: uuid.Must(uuid.NewV4()).String(), TargetAgentKey: "generalist",
		Status: models.DeliveryPlanStepAssignmentPending, CreatedAt: now, UpdatedAt: now,
	}

	expectReserveSnapshotPreamble(mock, execution, plan, steps, dependencies, parent, childTaskIDs)
	expectAssignmentRows(mock)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "delivery_plan_step_assignments" WHERE execution_id = \$1 AND status IN \(\$2,\$3,\$4,\$5\)`).
		WithArgs(execution.ID, models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
			models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps".*edge\.plan_id <> delivery_plan_steps\.plan_id.*NOT EXISTS.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(steps[1]))
	mockFrozenWorkspacesAndAgent(mock, *parent.DeliveryWorkItemID, now, assignment.TargetMachineID, "generalist", 1)
	mock.ExpectQuery(`INSERT INTO "delivery_plan_step_assignments"`).
		WithArgs(execution.ID, steps[1].ID, childTaskID, assignment.TargetMachineID, assignment.TargetAgentKey, models.DeliveryPlanStepAssignmentPending,
			nil, nil, nil, nil, now, now, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(assignmentID))

	reserved, err := ReserveReadyAssignmentsInTransaction(db, execution.ID, 1, childTaskIDs, now)
	if err != nil || len(reserved) != 1 || reserved[0].DeliveryPlanStepID != steps[1].ID {
		t.Fatalf("first reserve = (%+v, %v), want only DAG-ready step", reserved, err)
	}

	expectReserveSnapshotPreamble(mock, execution, plan, steps, dependencies, parent, childTaskIDs)
	expectAssignmentRows(mock, assignment)
	replayed, err := ReserveReadyAssignmentsInTransaction(db, execution.ID, 1, childTaskIDs, now.Add(time.Minute))
	if err != nil || len(replayed) != 1 || replayed[0].ID != assignment.ID || replayed[0].TargetMachineID != assignment.TargetMachineID || replayed[0].TargetAgentKey != assignment.TargetAgentKey {
		t.Fatalf("idempotent reserve = (%+v, %v), want existing assignment", replayed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type assignmentReservationFixture struct {
	MachineID string
	AgentKey  string
	Count     int
}

func assignmentReservationFixtures(assignments ...models.DeliveryPlanStepAssignment) []assignmentReservationFixture {
	counts := make(map[assignmentTargetKey]int)
	for _, assignment := range assignments {
		if !isActiveAssignmentStatus(assignment.Status) || assignment.TargetMachineID == "" || assignment.TargetAgentKey == "" {
			continue
		}
		key := assignmentTargetKey{MachineID: assignment.TargetMachineID, AgentKey: assignment.TargetAgentKey}
		counts[key]++
	}
	reservations := make([]assignmentReservationFixture, 0, len(counts))
	for key, count := range counts {
		reservations = append(reservations, assignmentReservationFixture{MachineID: key.MachineID, AgentKey: key.AgentKey, Count: count})
	}
	return reservations
}

func mockFrozenWorkspacesAndAgent(mock sqlmock.Sqlmock, workItemID uuid.UUID, now time.Time, machineID, agentKey string, concurrency int, reservations ...assignmentReservationFixture) {
	workerID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(`SELECT "kind","reference" FROM "delivery_context_snapshots" WHERE work_item_id = \$1 ORDER BY reference ASC`).
		WithArgs(workItemID).WillReturnRows(sqlmock.NewRows([]string{"kind", "reference"}).AddRow("repository", "workspace://backend"))
	mock.ExpectQuery(`(?s)SELECT heartbeat\.worker_id, heartbeat\.agent_key, heartbeat\.machine_id, heartbeat\.concurrency, heartbeat\.draining, heartbeat\.capabilities_json, heartbeat\.protocols_json, heartbeat\.workspace_readiness, heartbeat\.last_seen_at, profile\.operations_json AS profile_operations_json FROM automation_agent_heartbeats AS heartbeat JOIN automation_agent_profiles AS profile ON profile\.agent_key = heartbeat\.agent_key AND profile\.active = \$1 WHERE heartbeat\.last_seen_at >= \$2 AND heartbeat\.draining = \$3 AND heartbeat\.machine_id <> '' ORDER BY heartbeat\.machine_id ASC, heartbeat\.agent_key ASC, heartbeat\.worker_id ASC FOR UPDATE`).
		WithArgs(true, now.Add(-assignmentHeartbeatMaxAge), false).
		WillReturnRows(sqlmock.NewRows([]string{"worker_id", "agent_key", "machine_id", "concurrency", "draining", "capabilities_json", "protocols_json", "workspace_readiness", "last_seen_at", "profile_operations_json"}).
			AddRow(workerID.String(), agentKey, machineID, concurrency, false, `["delivery.implementation"]`, `["delivery.plan_steps.v1"]`, `[{"id":"backend","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`, now, `["delivery.implementation"]`))
	reservationRows := sqlmock.NewRows([]string{"target_machine_id", "target_agent_key", "reservation_count"})
	for _, reservation := range reservations {
		reservationRows.AddRow(reservation.MachineID, reservation.AgentKey, reservation.Count)
	}
	mock.ExpectQuery(`(?s)SELECT target_machine_id, target_agent_key, COUNT\(\*\) AS reservation_count FROM "delivery_plan_step_assignments" WHERE target_machine_id <> '' AND target_agent_key <> '' AND status IN \(.*\) GROUP BY target_machine_id, target_agent_key`).
		WithArgs(models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued, models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning).
		WillReturnRows(reservationRows)
}

func TestResolveAssignmentTargetsAccountsForReservationsFromAnotherPlan(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	workItemID := uuid.Must(uuid.NewV4())
	machineID := uuid.Must(uuid.NewV4()).String()
	planAID, planBID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	executionA := models.DeliveryPlanExecution{ID: uuid.Must(uuid.NewV4()), PlanID: planAID}
	executionB := models.DeliveryPlanExecution{ID: uuid.Must(uuid.NewV4()), PlanID: planBID}
	assignmentFromPlanA := models.DeliveryPlanStepAssignment{
		ExecutionID: executionA.ID, DeliveryPlanStepID: uuid.Must(uuid.NewV4()),
		TargetMachineID: machineID, TargetAgentKey: "generalist", Status: models.DeliveryPlanStepAssignmentRunning,
	}
	stepFromPlanB := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: executionB.PlanID, AgentKey: "generalist"}
	if executionA.PlanID == executionB.PlanID || !isActiveAssignmentStatus(assignmentFromPlanA.Status) {
		t.Fatal("fixture must model an active assignment owned by a different plan")
	}
	mockFrozenWorkspacesAndAgent(mock, workItemID, now, machineID, "generalist", 1, assignmentReservationFixtures(assignmentFromPlanA)...)

	if _, err := resolveAssignmentTargets(db, workItemID, []models.DeliveryPlanStep{stepFromPlanB}, now); !errors.Is(err, ErrNoEligibleAgentMachine) || !strings.Contains(err.Error(), "insufficient free concurrency") {
		t.Fatalf("second plan on a full capacity-1 machine = %v, want capacity block", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalAssignmentReleasesMachineProfileCapacity(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	workItemID := uuid.Must(uuid.NewV4())
	machineID := uuid.Must(uuid.NewV4()).String()
	released := models.DeliveryPlanStepAssignment{
		TargetMachineID: machineID, TargetAgentKey: "generalist", Status: models.DeliveryPlanStepAssignmentCompleted,
	}
	if isActiveAssignmentStatus(released.Status) {
		t.Fatal("completed assignment must not occupy a machine/profile slot")
	}
	step := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: uuid.Must(uuid.NewV4()), AgentKey: "generalist"}
	mockFrozenWorkspacesAndAgent(mock, workItemID, now, machineID, "generalist", 1, assignmentReservationFixtures(released)...)

	targets, err := resolveAssignmentTargets(db, workItemID, []models.DeliveryPlanStep{step}, now)
	if err != nil || targets[step.ID] != (assignmentTarget{MachineID: machineID, AgentKey: "generalist"}) {
		t.Fatalf("assignment after terminal release = (%+v, %v), want the freed machine/profile slot", targets, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredLeaseKeepsAssignmentReservedUntilTerminalRelease(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	expiredAt := now.Add(-time.Minute)
	task := models.AutomationTask{Status: "running", LeaseExpiresAt: &expiredAt}
	step := models.DeliveryPlanStep{Status: models.DeliveryPlanStepRunning, LeaseExpiresAt: &expiredAt}
	if err := validateAssignmentFailoverLease(task, step, now); err != nil {
		t.Fatalf("expired task and step leases should permit failover: %v", err)
	}

	machineID := uuid.Must(uuid.NewV4()).String()
	assignment := models.DeliveryPlanStepAssignment{
		TargetMachineID: machineID, TargetAgentKey: "generalist", Status: models.DeliveryPlanStepAssignmentRunning,
	}
	stepFromAnotherPlan := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: uuid.Must(uuid.NewV4()), AgentKey: "generalist"}
	reservations := assignmentReservationFixtures(assignment)
	if len(reservations) != 1 || reservations[0].Count != 1 {
		t.Fatalf("expired lease reservation = %+v, want one still-active logical assignment", reservations)
	}
	if _, err := allocateAssignmentTargets([]models.DeliveryPlanStep{stepFromAnotherPlan}, []*assignmentTargetCandidate{{
		Key: assignmentTargetKey{MachineID: machineID, AgentKey: "generalist"}, Capacity: 1, Reserved: reservations[0].Count,
	}}); !errors.Is(err, ErrNoEligibleAgentMachine) {
		t.Fatalf("second plan while expired assignment is recovering = %v, want occupied capacity", err)
	}

	assignment.Status = models.DeliveryPlanStepAssignmentCompleted
	if reservations := assignmentReservationFixtures(assignment); len(reservations) != 0 {
		t.Fatalf("terminal assignment reservations = %+v, want released capacity", reservations)
	}
	if _, err := allocateAssignmentTargets([]models.DeliveryPlanStep{stepFromAnotherPlan}, []*assignmentTargetCandidate{{
		Key: assignmentTargetKey{MachineID: machineID, AgentKey: "generalist"}, Capacity: 1,
	}}); err != nil {
		t.Fatalf("second plan after terminal release = %v, want available capacity", err)
	}
}

func TestAssignmentTargetAllocationFansOutAcrossReadyMachinesAndReplaysStably(t *testing.T) {
	steps := []models.DeliveryPlanStep{{ID: uuid.Must(uuid.NewV4()), AgentKey: "generalist"}, {ID: uuid.Must(uuid.NewV4()), AgentKey: "generalist"}}
	firstMachine, secondMachine := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	candidates := []*assignmentTargetCandidate{
		{Key: assignmentTargetKey{MachineID: firstMachine, AgentKey: "generalist"}, Capacity: 1},
		{Key: assignmentTargetKey{MachineID: secondMachine, AgentKey: "generalist"}, Capacity: 1},
	}
	targets, err := allocateAssignmentTargets(steps, candidates)
	if err != nil || len(targets) != 2 || targets[steps[0].ID].MachineID == targets[steps[1].ID].MachineID {
		t.Fatalf("target allocation = (%+v, %v), want two ready machines used in parallel", targets, err)
	}
	replayCandidates := []*assignmentTargetCandidate{
		{Key: candidates[0].Key, Capacity: candidates[0].Capacity},
		{Key: candidates[1].Key, Capacity: candidates[1].Capacity},
	}
	replayed, err := allocateAssignmentTargets(steps, replayCandidates)
	if err != nil || replayed[steps[0].ID] != targets[steps[0].ID] || replayed[steps[1].ID] != targets[steps[1].ID] {
		t.Fatalf("deterministic replay = (%+v, %v), want stable target pairs", replayed, err)
	}
}

func TestAssignmentTargetAllocationKeepsConstrainedProfileCapacityForIndependentSteps(t *testing.T) {
	// Display order deliberately puts the flexible step first. Without
	// constrained-first allocation it consumes the implementation-only slot,
	// and the later step falsely appears unschedulable despite two free workers.
	implementationMachine := "00000000-0000-4000-8000-000000000001"
	generalistMachine := "00000000-0000-4000-8000-000000000002"
	flexibleStep := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000003")), DisplayOrder: 1,
	}
	implementationStep := models.DeliveryPlanStep{
		ID: uuid.Must(uuid.FromString("00000000-0000-4000-8000-000000000004")), DisplayOrder: 2, AgentKey: "implementation",
	}
	candidates := []*assignmentTargetCandidate{
		{Key: assignmentTargetKey{MachineID: implementationMachine, AgentKey: "implementation"}, Capacity: 1},
		{Key: assignmentTargetKey{MachineID: generalistMachine, AgentKey: "generalist"}, Capacity: 1},
	}

	targets, err := allocateAssignmentTargets([]models.DeliveryPlanStep{flexibleStep, implementationStep}, candidates)
	if err != nil {
		t.Fatalf("feasible constrained + flexible parallel allocation = (%+v, %v)", targets, err)
	}
	if len(targets) != 2 || targets[implementationStep.ID] != (assignmentTarget{MachineID: implementationMachine, AgentKey: "implementation"}) || targets[flexibleStep.ID] != (assignmentTarget{MachineID: generalistMachine, AgentKey: "generalist"}) {
		t.Fatalf("targets = %+v, want constrained step on implementation and flexible step on generalist", targets)
	}
}

func TestAssignmentHeartbeatRequiresAllFrozenWorkspacesAndSandboxReadiness(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	heartbeat := assignmentHeartbeatRow{
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
		Concurrency: 2, LastSeenAt: now.Add(-30 * time.Second), CapabilitiesJSON: `["delivery.implementation"]`,
		ProtocolsJSON: `["delivery.plan_steps.v1"]`, ProfileOperationsJSON: `["delivery.implementation"]`,
		WorkspaceReadiness: `[{"id":"backend","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"},{"id":"frontend","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`,
	}
	if !assignmentHeartbeatEligible(heartbeat, []string{"backend", "frontend"}, "generalist", now) {
		t.Fatal("protocol-compatible machine with every frozen, Docker-ready workspace should be eligible")
	}
	legacyCapabilities := heartbeat
	legacyCapabilities.CapabilitiesJSON = `[]`
	if !assignmentHeartbeatEligible(legacyCapabilities, []string{"backend", "frontend"}, "generalist", now) {
		t.Fatal("legacy empty operation capabilities should remain compatible only when the runtime protocol is explicitly supported")
	}
	for _, mutate := range []func(*assignmentHeartbeatRow){
		func(row *assignmentHeartbeatRow) {
			row.WorkspaceReadiness = `[{"id":"backend","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`
		},
		func(row *assignmentHeartbeatRow) {
			row.WorkspaceReadiness = `[{"id":"backend","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"},{"id":"frontend","ready":false,"sandbox_ready":true,"isolation_mode":"docker_container"}]`
		},
		func(row *assignmentHeartbeatRow) {
			row.WorkspaceReadiness = `[{"id":"backend","ready":true,"sandbox_ready":true,"isolation_mode":"host_process"},{"id":"frontend","ready":true,"sandbox_ready":true,"isolation_mode":"docker_container"}]`
		},
		func(row *assignmentHeartbeatRow) { row.LastSeenAt = now.Add(-assignmentHeartbeatMaxAge - time.Second) },
		func(row *assignmentHeartbeatRow) { row.Draining = true },
		func(row *assignmentHeartbeatRow) { row.ProfileOperationsJSON = `[]` },
		func(row *assignmentHeartbeatRow) { row.CapabilitiesJSON = `["delivery.plan"]` },
		func(row *assignmentHeartbeatRow) { row.ProtocolsJSON = `[]` },
		func(row *assignmentHeartbeatRow) { row.ProtocolsJSON = `["delivery.plan_steps.v0"]` },
		func(row *assignmentHeartbeatRow) { row.ProtocolsJSON = `["delivery.plan_steps.v2"]` },
		func(row *assignmentHeartbeatRow) { row.ProtocolsJSON = `["delivery.plan_steps.v1","unknown.future"]` },
		func(row *assignmentHeartbeatRow) {
			row.ProtocolsJSON = `["delivery.plan_steps.v1","delivery.plan_steps.v1"]`
		},
		func(row *assignmentHeartbeatRow) { row.ProtocolsJSON = `not-json` },
	} {
		candidate := heartbeat
		mutate(&candidate)
		if assignmentHeartbeatEligible(candidate, []string{"backend", "frontend"}, "generalist", now) {
			t.Errorf("incompatible heartbeat must be excluded: %+v", candidate)
		}
	}
}

func TestValidateStepProtocolWorkerRequiresFreshAuthenticatedRuntimeProtocol(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	workerID, instanceID, machineID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()).String()
	for _, test := range []struct {
		name      string
		protocols string
		draining  bool
		lastSeen  time.Time
		wantErr   bool
	}{
		{name: "compatible current runtime", protocols: `["delivery.plan_steps.v1"]`, lastSeen: now},
		{name: "legacy heartbeat", protocols: `[]`, lastSeen: now, wantErr: true},
		{name: "unknown protocol", protocols: `["delivery.plan_steps.v2"]`, lastSeen: now, wantErr: true},
		{name: "unknown alongside required", protocols: `["delivery.plan_steps.v1","future.protocol"]`, lastSeen: now, wantErr: true},
		{name: "draining runtime", protocols: `["delivery.plan_steps.v1"]`, draining: true, lastSeen: now, wantErr: true},
		{name: "stale runtime", protocols: `["delivery.plan_steps.v1"]`, lastSeen: now.Add(-assignmentHeartbeatMaxAge - time.Second), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newOrchestrationTestDB(t)
			mock.ExpectQuery(`SELECT protocols_json, draining, last_seen_at FROM "automation_agent_heartbeats" WHERE worker_id = \$1 AND agent_key = \$2 AND machine_id = \$3 AND agent_instance_id = \$4 AND last_seen_at >= \$5 AND last_seen_at <= \$6 LIMIT \$7`).
				WithArgs(workerID, "generalist", machineID, instanceID, now.Add(-assignmentHeartbeatMaxAge), now.Add(5*time.Minute), 1).
				WillReturnRows(sqlmock.NewRows([]string{"protocols_json", "draining", "last_seen_at"}).AddRow(test.protocols, test.draining, test.lastSeen))

			err := ValidateStepProtocolWorker(db, workerID, "generalist", machineID, instanceID, now)
			if test.wantErr && !errors.Is(err, ErrNoEligibleAgentMachine) {
				t.Fatalf("legacy/ineligible runtime error = %v, want ErrNoEligibleAgentMachine", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("compatible runtime was rejected: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAssignmentTargetBlocksWhenNoReadyMachineHasEnoughSlots(t *testing.T) {
	steps := []models.DeliveryPlanStep{{ID: uuid.Must(uuid.NewV4())}, {ID: uuid.Must(uuid.NewV4())}}
	candidates := []*assignmentTargetCandidate{{Key: assignmentTargetKey{MachineID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist"}, Capacity: 1}}
	if _, err := allocateAssignmentTargets(steps, candidates); !errors.Is(err, ErrNoEligibleAgentMachine) || !strings.Contains(err.Error(), "insufficient free concurrency") {
		t.Fatalf("insufficient machine capacity error = %v, want explained retryable block", err)
	}
}

func TestReserveReadyAssignmentsRespectsMaxConcurrency(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	execution := orchestrationExecution(plan, steps, dependencies, parent, 2, now)
	childTaskIDs := map[uuid.UUID]uuid.UUID{steps[1].ID: uuid.Must(uuid.NewV4())}

	expectReserveSnapshotPreamble(mock, execution, plan, steps, dependencies, parent, childTaskIDs)
	expectAssignmentRows(mock)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "delivery_plan_step_assignments" WHERE execution_id = \$1 AND status IN \(\$2,\$3,\$4,\$5\)`).
		WithArgs(execution.ID, models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
			models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	if _, err := ReserveReadyAssignmentsInTransaction(db, execution.ID, 1, childTaskIDs, now); !errors.Is(err, ErrPlanExecutionCapacity) {
		t.Fatalf("full execution capacity error = %v, want ErrPlanExecutionCapacity", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReserveReadyAssignmentsRejectsMappingAboveRequestedLimit(t *testing.T) {
	db, _ := newOrchestrationTestDB(t)
	_, steps, _, _, now := orchestrationFixture()
	childTaskIDs := map[uuid.UUID]uuid.UUID{
		steps[0].ID: uuid.Must(uuid.NewV4()),
		steps[1].ID: uuid.Must(uuid.NewV4()),
	}
	if _, err := ReserveReadyAssignmentsInTransaction(db, uuid.Must(uuid.NewV4()), 1, childTaskIDs, now); !errors.Is(err, ErrStepInputInvalid) {
		t.Fatalf("mapping above limit error = %v, want ErrStepInputInvalid", err)
	}
}

func TestReserveReadyAssignmentsRejectsChildFromDifferentOrganization(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	execution := orchestrationExecution(plan, steps, dependencies, parent, 2, now)
	childTaskID := uuid.Must(uuid.NewV4())
	childTaskIDs := map[uuid.UUID]uuid.UUID{steps[1].ID: childTaskID}
	expectReserveExecutionIdentityAndLocks(mock, execution, parent)
	projectID, clientID, otherClientID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	expectTaskOrganizationScope(mock, parent.ID, *parent.DeliveryWorkItemID, projectID, clientID)
	expectPlanSnapshot(mock, plan, steps, dependencies)
	expectTaskOrganizationScope(mock, childTaskID, *parent.DeliveryWorkItemID, projectID, otherClientID)

	if _, err := ReserveReadyAssignmentsInTransaction(db, execution.ID, 1, childTaskIDs, now); !errors.Is(err, ErrPlanExecutionParentMismatch) {
		t.Fatalf("cross-organization child error = %v, want ErrPlanExecutionParentMismatch", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReserveReadyAssignmentsRejectsUnmetDependency(t *testing.T) {
	db, mock := newOrchestrationTestDB(t)
	plan, steps, dependencies, parent, now := orchestrationFixture()
	steps[0].Status = models.DeliveryPlanStepReady
	execution := orchestrationExecution(plan, steps, dependencies, parent, 2, now)
	childTaskIDs := map[uuid.UUID]uuid.UUID{steps[1].ID: uuid.Must(uuid.NewV4())}

	expectReserveSnapshotPreamble(mock, execution, plan, steps, dependencies, parent, childTaskIDs)
	expectAssignmentRows(mock)
	mock.ExpectQuery(`SELECT count\(\*\) FROM "delivery_plan_step_assignments" WHERE execution_id = \$1 AND status IN \(\$2,\$3,\$4,\$5\)`).
		WithArgs(execution.ID, models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
			models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps".*NOT EXISTS.*NOT EXISTS.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	if _, err := ReserveReadyAssignmentsInTransaction(db, execution.ID, 1, childTaskIDs, now); !errors.Is(err, ErrPlanExecutionConflict) {
		t.Fatalf("unmet dependency error = %v, want ErrPlanExecutionConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
