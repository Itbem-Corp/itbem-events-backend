package deliveryplansteps

import (
	"fmt"
	"sort"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AggregationPendingReason describes the artifact which is deliberately not
// inferred from child completion. Each child currently produces an isolated
// worktree; the control plane has no verified merge artifact or full-plan
// acceptance receipt with which to promote the parent execution.
const AggregationPendingReason = "aggregation_pending: child worktrees have not been merged into a verified parent artifact and full-plan acceptance evidence"

// ChildAutomationTaskID and ChildAutomationJobID make materialization of one
// step child stable across dispatcher retries. The execution identity scopes
// both values to the immutable approved-plan attempt.
func ChildAutomationTaskID(executionID, stepID uuid.UUID) uuid.UUID {
	return uuid.NewV5(uuid.NamespaceURL, "itbem/delivery-plan-step-task/"+executionID.String()+"/"+stepID.String())
}

func ChildAutomationJobID(executionID, stepID uuid.UUID) uuid.UUID {
	return uuid.NewV5(uuid.NamespaceURL, "itbem/delivery-plan-step-job/"+executionID.String()+"/"+stepID.String())
}

type FanInDecision struct {
	WaitingForChildren bool
	HasChildFailure    bool
	AggregationPending bool
	Reason             string
}

// AssessFanIn never declares an execution successful based on child rows
// alone. It waits while assigned children remain active, blocks on any failed
// child or missing assignment, and otherwise reports the absent aggregate
// artifact explicitly. A future caller may add a verified aggregate receipt;
// this contract intentionally has no optimistic success branch today.
func AssessFanIn(requiredStepIDs []uuid.UUID, assignments []models.DeliveryPlanStepAssignment) FanInDecision {
	required := make(map[uuid.UUID]struct{}, len(requiredStepIDs))
	for _, stepID := range requiredStepIDs {
		if stepID == uuid.Nil {
			return FanInDecision{AggregationPending: true, Reason: AggregationPendingReason + ": invalid plan step identity"}
		}
		required[stepID] = struct{}{}
	}
	seen := make(map[uuid.UUID]models.DeliveryPlanStepAssignment, len(assignments))
	active, failed := false, false
	for _, assignment := range assignments {
		if _, exists := required[assignment.DeliveryPlanStepID]; !exists || assignment.ID == uuid.Nil || assignment.ChildAutomationTaskID == uuid.Nil {
			return FanInDecision{AggregationPending: true, Reason: AggregationPendingReason + ": assignment does not match the frozen plan"}
		}
		if _, duplicate := seen[assignment.DeliveryPlanStepID]; duplicate {
			return FanInDecision{AggregationPending: true, Reason: AggregationPendingReason + ": duplicate step assignment"}
		}
		seen[assignment.DeliveryPlanStepID] = assignment
		switch assignment.Status {
		case models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
			models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning:
			active = true
		case models.DeliveryPlanStepAssignmentFailed, models.DeliveryPlanStepAssignmentBlocked, models.DeliveryPlanStepAssignmentCancelled:
			failed = true
		case models.DeliveryPlanStepAssignmentCompleted:
		default:
			return FanInDecision{AggregationPending: true, Reason: AggregationPendingReason + ": assignment has an unsupported state"}
		}
	}
	if active {
		decision := FanInDecision{WaitingForChildren: true, HasChildFailure: failed}
		if failed {
			decision.Reason = AggregationPendingReason + ": one or more child assignments did not complete successfully"
		}
		return decision
	}
	if failed {
		return FanInDecision{HasChildFailure: true, AggregationPending: true, Reason: AggregationPendingReason + ": one or more child assignments did not complete successfully"}
	}
	if len(seen) != len(required) {
		return FanInDecision{AggregationPending: true, Reason: AggregationPendingReason + ": ready dependent plan steps have not yet been assigned"}
	}
	return FanInDecision{AggregationPending: true, Reason: AggregationPendingReason}
}

// ReadyPlanStepsInTransaction returns unassigned plan steps whose direct
// dependencies are all completed, capped by the execution's free assignment
// capacity. The historical name is kept for API compatibility. Each child
// applies and verifies its dependencies' signed patch manifest before it can
// complete, so readiness may advance across machines without sharing a
// worktree. It serializes scheduling using the same parent -> execution lock
// order as ReserveReadyAssignmentsInTransaction.
func ReadyPlanStepsInTransaction(tx *gorm.DB, executionID uuid.UUID, _ time.Time) (models.DeliveryPlanExecution, []models.DeliveryPlanStep, error) {
	if tx == nil || executionID == uuid.Nil {
		return models.DeliveryPlanExecution{}, nil, invalidStepInput("transaction and execution_id are required")
	}
	var identity models.DeliveryPlanExecution
	if err := tx.Select("id", "automation_task_id").Take(&identity, "id = ?", executionID).Error; err != nil {
		return models.DeliveryPlanExecution{}, nil, err
	}
	var parent models.AutomationTask
	if err := tx.Select("id", "delivery_work_item_id").Clauses(clause.Locking{Strength: "UPDATE"}).First(&parent, "id = ?", identity.AutomationTaskID).Error; err != nil {
		return models.DeliveryPlanExecution{}, nil, err
	}
	var execution models.DeliveryPlanExecution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&execution, "id = ?", executionID).Error; err != nil {
		return models.DeliveryPlanExecution{}, nil, err
	}
	if execution.AutomationTaskID != parent.ID || parent.DeliveryWorkItemID == nil {
		return models.DeliveryPlanExecution{}, nil, ErrPlanExecutionParentMismatch
	}
	if execution.Status != models.DeliveryPlanExecutionPending && execution.Status != models.DeliveryPlanExecutionDispatching && execution.Status != models.DeliveryPlanExecutionRunning {
		return models.DeliveryPlanExecution{}, nil, ErrPlanExecutionConflict
	}
	if execution.MaxConcurrency < 1 || execution.MaxConcurrency > models.DeliveryPlanExecutionMaxConcurrency {
		return models.DeliveryPlanExecution{}, nil, invalidStepInput("persisted execution max_concurrency is invalid")
	}
	var activeCount int64
	if err := tx.Model(&models.DeliveryPlanStepAssignment{}).
		Where("execution_id = ? AND status IN ?", execution.ID, []string{
			models.DeliveryPlanStepAssignmentPending, models.DeliveryPlanStepAssignmentQueued,
			models.DeliveryPlanStepAssignmentDispatched, models.DeliveryPlanStepAssignmentRunning,
		}).Count(&activeCount).Error; err != nil {
		return models.DeliveryPlanExecution{}, nil, err
	}
	capacity := execution.MaxConcurrency - int(activeCount)
	if capacity <= 0 {
		return execution, []models.DeliveryPlanStep{}, nil
	}
	var steps []models.DeliveryPlanStep
	err := tx.Model(&models.DeliveryPlanStep{}).
		Where("plan_id = ? AND status IN ?", execution.PlanID, []string{models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady}).
		Where(`NOT EXISTS (
			SELECT 1 FROM delivery_plan_step_dependencies AS edge
			JOIN delivery_plan_steps AS dependency ON dependency.id = edge.depends_on_step_id
			WHERE edge.step_id = delivery_plan_steps.id
			AND (edge.plan_id <> delivery_plan_steps.plan_id OR dependency.plan_id <> delivery_plan_steps.plan_id OR dependency.status <> ?)
		)`, models.DeliveryPlanStepCompleted).
		Where(`NOT EXISTS (
			SELECT 1 FROM delivery_plan_step_assignments AS assignment
			WHERE assignment.execution_id = ? AND assignment.delivery_plan_step_id = delivery_plan_steps.id
		)`, execution.ID).
		Order("display_order ASC, id ASC").Limit(capacity).
		Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&steps).Error
	if err != nil {
		return models.DeliveryPlanExecution{}, nil, err
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].DisplayOrder != steps[j].DisplayOrder {
			return steps[i].DisplayOrder < steps[j].DisplayOrder
		}
		return steps[i].ID.String() < steps[j].ID.String()
	})
	if len(steps) > capacity {
		return models.DeliveryPlanExecution{}, nil, fmt.Errorf("%w: root dispatch exceeded execution capacity", ErrPlanExecutionCapacity)
	}
	return execution, steps, nil
}

// ReadyRootStepsInTransaction is retained for older callers; readiness now
// includes any step whose dependencies have completed and whose output can be
// fetched and verified by the assigned worker.
func ReadyRootStepsInTransaction(tx *gorm.DB, executionID uuid.UUID, now time.Time) (models.DeliveryPlanExecution, []models.DeliveryPlanStep, error) {
	return ReadyPlanStepsInTransaction(tx, executionID, now)
}
