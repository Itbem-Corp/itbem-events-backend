package deliveryplansteps

import (
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
)

func TestAssessFanInNeverTreatsChildCompletionAsAggregateSuccess(t *testing.T) {
	stepA, stepB := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	assignments := []models.DeliveryPlanStepAssignment{
		{ID: uuid.Must(uuid.NewV4()), DeliveryPlanStepID: stepA, ChildAutomationTaskID: uuid.Must(uuid.NewV4()), Status: models.DeliveryPlanStepAssignmentCompleted},
		{ID: uuid.Must(uuid.NewV4()), DeliveryPlanStepID: stepB, ChildAutomationTaskID: uuid.Must(uuid.NewV4()), Status: models.DeliveryPlanStepAssignmentCompleted},
	}
	decision := AssessFanIn([]uuid.UUID{stepA, stepB}, assignments)
	if decision.WaitingForChildren || !decision.AggregationPending || decision.HasChildFailure {
		t.Fatalf("completed children without an aggregate artifact must remain pending: %#v", decision)
	}
	if !strings.Contains(decision.Reason, "aggregation_pending") {
		t.Fatalf("pending decision must carry an explicit auditable marker: %#v", decision)
	}
}

func TestAssessFanInWaitsForActiveChildrenAndDoesNotHidePartialFailure(t *testing.T) {
	stepA, stepB := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	assignments := []models.DeliveryPlanStepAssignment{
		{ID: uuid.Must(uuid.NewV4()), DeliveryPlanStepID: stepA, ChildAutomationTaskID: uuid.Must(uuid.NewV4()), Status: models.DeliveryPlanStepAssignmentFailed},
		{ID: uuid.Must(uuid.NewV4()), DeliveryPlanStepID: stepB, ChildAutomationTaskID: uuid.Must(uuid.NewV4()), Status: models.DeliveryPlanStepAssignmentRunning},
	}
	decision := AssessFanIn([]uuid.UUID{stepA, stepB}, assignments)
	if !decision.WaitingForChildren || !decision.HasChildFailure || decision.AggregationPending {
		t.Fatalf("fan-in must drain already dispatched siblings without claiming success/final failure early: %#v", decision)
	}
}

func TestAssessFanInBlocksUndispatchedDependentWorkAndInvalidAssignments(t *testing.T) {
	root, dependent := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	rootAssignment := models.DeliveryPlanStepAssignment{
		ID: uuid.Must(uuid.NewV4()), DeliveryPlanStepID: root, ChildAutomationTaskID: uuid.Must(uuid.NewV4()), Status: models.DeliveryPlanStepAssignmentCompleted,
	}
	decision := AssessFanIn([]uuid.UUID{root, dependent}, []models.DeliveryPlanStepAssignment{rootAssignment})
	if decision.WaitingForChildren || !decision.AggregationPending || !strings.Contains(decision.Reason, "dependent plan steps") {
		t.Fatalf("an undispatched dependent must stop at the merge barrier: %#v", decision)
	}
	duplicate := rootAssignment
	duplicate.ID = uuid.Must(uuid.NewV4())
	decision = AssessFanIn([]uuid.UUID{root}, []models.DeliveryPlanStepAssignment{rootAssignment, duplicate})
	if !decision.AggregationPending || !strings.Contains(decision.Reason, "duplicate") {
		t.Fatalf("duplicate assignment must fail closed: %#v", decision)
	}
}

func TestReadyPlanStepsCanDispatchDependentAfterParentCompletion(t *testing.T) {
	plan, steps, _, parent, now := orchestrationFixture()
	steps[0].Status = models.DeliveryPlanStepCompleted
	steps[1].Status = models.DeliveryPlanStepPlanned
	execution := models.DeliveryPlanExecution{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parent.ID, PlanID: plan.ID,
		MaxConcurrency: 2, Status: models.DeliveryPlanExecutionRunning,
	}
	db, mock := newOrchestrationTestDB(t)
	mock.ExpectQuery(`SELECT "id","automation_task_id" FROM "delivery_plan_executions" WHERE id = \$1 LIMIT \$2`).
		WithArgs(execution.ID, 1).WillReturnRows(sqlmock.NewRows([]string{"id", "automation_task_id"}).AddRow(execution.ID, parent.ID))
	expectOrchestrationParentLock(mock, parent)
	mock.ExpectQuery(`SELECT \* FROM "delivery_plan_executions" WHERE id = \$1 ORDER BY "delivery_plan_executions"\."id" LIMIT \$2 FOR UPDATE`).
		WithArgs(execution.ID, 1).WillReturnRows(executionRows(execution))
	mock.ExpectQuery(`SELECT count\(\*\) FROM "delivery_plan_step_assignments" WHERE execution_id = \$1 AND status IN \(.*\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)SELECT \* FROM "delivery_plan_steps" WHERE .*status IN \(.*\).*delivery_plan_step_dependencies AS edge.*dependency.status <> \$[0-9]+.*delivery_plan_step_assignments AS assignment.*FOR UPDATE SKIP LOCKED`).
		WillReturnRows(planStepClaimRows(steps[1]))

	_, ready, err := ReadyPlanStepsInTransaction(db, execution.ID, now)
	if err != nil || len(ready) != 1 || ready[0].ID != steps[1].ID {
		t.Fatalf("ready dependent steps = %#v, err=%v; want step %s after its parent completed", ready, err, steps[1].ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStepChildIDsAreStableAcrossOutboxRedelivery(t *testing.T) {
	executionID, stepID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	if ChildAutomationTaskID(executionID, stepID) != ChildAutomationTaskID(executionID, stepID) || ChildAutomationJobID(executionID, stepID) != ChildAutomationJobID(executionID, stepID) {
		t.Fatal("retrying plan-step materialization must preserve deterministic child and outbox identities")
	}
	if ChildAutomationTaskID(executionID, stepID) == ChildAutomationTaskID(uuid.Must(uuid.NewV4()), stepID) || ChildAutomationJobID(executionID, stepID) == ChildAutomationJobID(executionID, uuid.Must(uuid.NewV4())) {
		t.Fatal("child identities must be scoped to the execution and step")
	}
}
