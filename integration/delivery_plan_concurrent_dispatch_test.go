//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/deliveryplansteps"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestConcurrentPlanDispatchesIndependentReadyStepsAndGatesDependent exercises
// the production reservation transaction from simultaneous dispatcher calls.
// Each run gets a private PostgreSQL schema copied from the production table
// definitions; the schema is dropped even on assertion failure. Independent
// transactions must commit their reservations for the competing dispatcher to
// observe them, so an enclosing rollback cannot provide isolation here.
func TestConcurrentPlanDispatchesIndependentReadyStepsAndGatesDependent(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:16]
	schema := "itbem_dispatch_" + suffix
	require.NoError(t, db.Exec(`CREATE SCHEMA "`+schema+`"`).Error)
	t.Cleanup(func() {
		if err := db.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error; err != nil {
			t.Errorf("drop isolated dispatcher schema: %v", err)
		}
	})
	for _, table := range []string{
		"client_types", "clients", "delivery_projects", "delivery_work_items", "delivery_context_snapshots",
		"delivery_gates", "delivery_plans", "delivery_plan_steps", "delivery_plan_step_dependencies",
		"automation_tasks", "delivery_plan_executions", "delivery_plan_step_assignments",
		"automation_agent_profiles", "automation_agent_heartbeats",
	} {
		statement := `CREATE TABLE "` + schema + `"."` + table + `" (LIKE public."` + table + `" INCLUDING ALL)`
		require.NoErrorf(t, db.Exec(statement).Error, "clone isolated table %s", table)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	parentTaskID := uuid.Must(uuid.NewV4())
	workItemID := uuid.Must(uuid.NewV4())
	planID := uuid.Must(uuid.NewV4())
	gateID := uuid.Must(uuid.NewV4())
	stepOne := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: planID, StepKey: "root-one", IdempotencyKey: "dispatch-one-" + suffix, Role: models.DeliveryPlanStepRoleImplementation, DisplayOrder: 1, Title: "First ready root", Objective: "Reserve one frozen step once", AcceptanceCriteriaJSON: `["root one complete"]`, EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepPlanned, CreatedBy: "integration", CreatedAt: now, UpdatedAt: now}
	stepTwo := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: planID, StepKey: "root-two", IdempotencyKey: "dispatch-two-" + suffix, Role: models.DeliveryPlanStepRoleImplementation, DisplayOrder: 2, Title: "Second ready root", Objective: "Compete for the remaining execution slot", AcceptanceCriteriaJSON: `["root two complete"]`, EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepPlanned, CreatedBy: "integration", CreatedAt: now, UpdatedAt: now}
	integrationStep := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: planID, StepKey: "integrate", IdempotencyKey: "dispatch-integrate-" + suffix, Role: models.DeliveryPlanStepRoleIntegration, DisplayOrder: 3, Title: "Final integration", Objective: "Wait for both implementation roots", AcceptanceCriteriaJSON: `["root one complete","root two complete"]`, EvidenceRequirementsJSON: `[]`, Status: models.DeliveryPlanStepPlanned, CreatedBy: "integration", CreatedAt: now, UpdatedAt: now}
	children := []models.AutomationTask{
		concurrentDispatchChild(workItemID, "step-one", now),
		concurrentDispatchChild(workItemID, "conflicting-step-one", now),
		concurrentDispatchChild(workItemID, "step-two", now),
		concurrentDispatchChild(workItemID, "integration", now),
	}
	workers := []planFanoutWorker{
		makePlanFanoutWorker(t, "dispatchalpha", "dispatch-alpha", workItemID, "integration-dispatch", now),
		makePlanFanoutWorker(t, "dispatchbeta", "dispatch-beta", workItemID, "integration-dispatch", now),
	}
	var execution models.DeliveryPlanExecution
	setupErr := db.Transaction(func(tx *gorm.DB) error {
		if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
			return err
		}
		clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Concurrent dispatch type " + suffix, Code: "CDT_" + strings.ToUpper(suffix), Level: 10, IsActive: true, CreatedAt: now, UpdatedAt: now}
		client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Concurrent dispatch client " + suffix, Code: "concurrent-dispatch-" + suffix, ClientTypeID: clientType.ID, IsActive: true, CreatedAt: now, UpdatedAt: now}
		project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Concurrent dispatch " + suffix, Slug: "concurrent-dispatch-" + suffix, Summary: "Isolated dispatcher race fixture", Status: "active", CreatedBy: "integration", CreatedAt: now, UpdatedAt: now}
		workItem := models.DeliveryWorkItem{ID: workItemID, ProjectID: project.ID, RequestedBy: "integration-dispatch", Title: "Concurrent frozen-plan dispatch", Description: "Exercise competing reservation transactions", ExpectedOutcome: "One assignment per frozen step within execution capacity", IncludedScopeJSON: `[]`, ExcludedScopeJSON: `[]`, AcceptanceJSON: `[]`, ClientContextJSON: `{}`, PlanJSON: `{}`, State: "implementation", CreatedAt: now, UpdatedAt: now}
		snapshot := models.DeliveryContextSnapshot{ID: uuid.Must(uuid.NewV4()), WorkItemID: workItemID, SourceID: uuid.Must(uuid.NewV4()), Kind: "repository", Name: "Disposable local workspace", Reference: "workspace://fanoutfixture", MetadataJSON: `{}`, CapturedAt: now, CreatedAt: now}
		gate := models.DeliveryGate{ID: gateID, WorkItemID: workItemID, Kind: "plan", Decision: "approved", DecidedBy: "integration-reviewer", EvidenceChecklist: `[]`, DecidedAt: now, CreatedAt: now}
		plan := models.DeliveryPlan{ID: planID, WorkItemID: workItemID, Version: 1, Status: "approved", Summary: "Two independent roots and one dependent step", StructuredJSON: `{}`, ContextDigest: "concurrent-dispatch-v1", ProposedBy: "integration", ApprovedGateID: &gateID, CreatedAt: now}
		parent := models.AutomationTask{ID: parentTaskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: "integration-dispatch", DeliveryWorkItemID: &workItemID, CorrelationID: workItemID.String(), Operation: "delivery.implementation", InputRef: "private://concurrent-dispatch/parent", Status: "queued", CreatedAt: now, UpdatedAt: now}
		for _, value := range []any{&clientType, &client, &project, &workItem, &snapshot, &gate, &plan, &stepOne, &stepTwo, &integrationStep, &parent} {
			if err := tx.Create(value).Error; err != nil {
				return fmt.Errorf("create isolated dispatcher fixture %T: %w", value, err)
			}
		}
		for _, dependency := range []models.DeliveryPlanStepDependency{
			{ID: uuid.Must(uuid.NewV4()), PlanID: planID, StepID: integrationStep.ID, DependsOnStepID: stepOne.ID, CreatedAt: now},
			{ID: uuid.Must(uuid.NewV4()), PlanID: planID, StepID: integrationStep.ID, DependsOnStepID: stepTwo.ID, CreatedAt: now},
		} {
			if err := tx.Create(&dependency).Error; err != nil {
				return fmt.Errorf("create integration-step dependency: %w", err)
			}
		}
		for i := range workers {
			if err := tx.Create(&workers[i].profile).Error; err != nil {
				return fmt.Errorf("create dispatcher profile: %w", err)
			}
			if err := tx.Create(&workers[i].heartbeat).Error; err != nil {
				return fmt.Errorf("create dispatcher heartbeat: %w", err)
			}
		}
		for i := range children {
			if err := tx.Create(&children[i]).Error; err != nil {
				return fmt.Errorf("create reserved child task: %w", err)
			}
		}
		created, _, err := deliveryplansteps.CreateExecutionInTransaction(tx, parentTaskID, planID, 2, "concurrent-dispatch-"+suffix, now)
		if err != nil {
			return fmt.Errorf("freeze approved plan for concurrent dispatch: %w", err)
		}
		execution = created
		return nil
	})
	require.NoError(t, setupErr, "prepare isolated frozen plan and eligible local workers")
	require.Equal(t, 2, execution.MaxConcurrency, "the concurrency ceiling must be frozen on the execution")
	initiallyReady := concurrentDispatchReadySteps(t, db, schema, execution.ID)
	require.ElementsMatch(t, []uuid.UUID{stepOne.ID, stepTwo.ID}, concurrentDispatchStepIDs(initiallyReady),
		"only dependency-free roots should be ready before dispatch")

	// Two dispatchers concurrently reserve separate dependency-free roots. Both
	// must commit under the execution's max_concurrency=2 and be routed to
	// distinct eligible local machines rather than serialized onto one worker.
	parallelResults := raceConcurrentDispatchReservations(t, db, schema, execution.ID, []concurrentDispatchRequest{
		{stepID: stepOne.ID, childTaskID: children[0].ID},
		{stepID: stepTwo.ID, childTaskID: children[2].ID},
	})
	assertAllConcurrentDispatchesSucceeded(t, parallelResults)
	parallelAssignments := assertConcurrentDispatchRows(t, db, schema, execution.ID, 2, []uuid.UUID{stepOne.ID, stepTwo.ID, integrationStep.ID})
	assignmentByStep := make(map[uuid.UUID]models.DeliveryPlanStepAssignment, len(parallelAssignments))
	for _, assignment := range parallelAssignments {
		assignmentByStep[assignment.DeliveryPlanStepID] = assignment
	}
	require.Len(t, assignmentByStep, 2)
	rootOneAssignment := assignmentByStep[stepOne.ID]
	rootTwoAssignment := assignmentByStep[stepTwo.ID]
	require.Equal(t, children[0].ID, rootOneAssignment.ChildAutomationTaskID)
	require.Equal(t, children[2].ID, rootTwoAssignment.ChildAutomationTaskID)
	require.NotEqual(t, rootOneAssignment.TargetMachineID, rootTwoAssignment.TargetMachineID,
		"independent steps should be dispatched to separate local machines when both have capacity")
	require.NotEqual(t, rootOneAssignment.TargetAgentKey, rootTwoAssignment.TargetAgentKey)

	// Replaying the same reservation concurrently is idempotent: both callers
	// observe the same durable row and no second assignment is created. A
	// competing child-task binding is rejected rather than silently reassigned.
	replayResults := raceConcurrentDispatchReservations(t, db, schema, execution.ID, []concurrentDispatchRequest{
		{stepID: stepOne.ID, childTaskID: children[0].ID},
		{stepID: stepOne.ID, childTaskID: children[0].ID},
	})
	assertAllConcurrentDispatchesSucceeded(t, replayResults)
	require.Equal(t, replayResults[0].rows[0].ID, replayResults[1].rows[0].ID,
		"concurrent retries must return the same assignment identity")
	var conflictingRows []models.DeliveryPlanStepAssignment
	conflictErr := db.Transaction(func(tx *gorm.DB) error {
		if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
			return err
		}
		rows, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, execution.ID, 1, map[uuid.UUID]uuid.UUID{stepOne.ID: children[1].ID}, time.Now().UTC())
		conflictingRows = rows
		return err
	})
	require.ErrorIs(t, conflictErr, deliveryplansteps.ErrPlanExecutionConflict)
	require.Empty(t, conflictingRows)
	assertConcurrentDispatchRows(t, db, schema, execution.ID, 2, []uuid.UUID{stepOne.ID, stepTwo.ID, integrationStep.ID})

	// Complete only one root to free an execution slot, then prove the scheduler
	// still blocks the dependent because its other prerequisite remains open.
	markConcurrentDispatchRootComplete(t, db, schema, execution.ID, stepOne.ID)
	readyAfterOne := concurrentDispatchReadySteps(t, db, schema, execution.ID)
	require.Empty(t, readyAfterOne,
		"the assigned remaining root and its dependent must stay unready until every dependency is completed")
	dependentAttemptErr := reserveConcurrentDispatchDependent(t, db, schema, execution.ID, integrationStep.ID, children[3].ID)
	require.ErrorIs(t, dependentAttemptErr, deliveryplansteps.ErrPlanExecutionConflict)

	markConcurrentDispatchRootComplete(t, db, schema, execution.ID, stepTwo.ID)
	readyAfterBoth := concurrentDispatchReadySteps(t, db, schema, execution.ID)
	require.ElementsMatch(t, []uuid.UUID{integrationStep.ID}, concurrentDispatchStepIDs(readyAfterBoth),
		"the dependent should become ready once both approved prerequisites complete")
	dependentAssignments, dependentErr := reserveConcurrentDispatchDependentWithRows(t, db, schema, execution.ID, integrationStep.ID, children[3].ID)
	require.NoError(t, dependentErr)
	require.Len(t, dependentAssignments, 1)
	require.Equal(t, integrationStep.ID, dependentAssignments[0].DeliveryPlanStepID)
	require.Equal(t, children[3].ID, dependentAssignments[0].ChildAutomationTaskID)

	// An exact retry remains idempotent after the dependency opens.
	replayedDependent, replayDependentErr := reserveConcurrentDispatchDependentWithRows(t, db, schema, execution.ID, integrationStep.ID, children[3].ID)
	require.NoError(t, replayDependentErr)
	require.Len(t, replayedDependent, 1)
	require.Equal(t, dependentAssignments[0].ID, replayedDependent[0].ID)
	assertConcurrentDispatchRows(t, db, schema, execution.ID, 3, []uuid.UUID{stepOne.ID, stepTwo.ID, integrationStep.ID})
}

type concurrentDispatchRequest struct {
	stepID      uuid.UUID
	childTaskID uuid.UUID
}

type concurrentDispatchResult struct {
	request concurrentDispatchRequest
	rows    []models.DeliveryPlanStepAssignment
	err     error
}

func concurrentDispatchChild(workItemID uuid.UUID, label string, now time.Time) models.AutomationTask {
	return models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), JobID: uuid.Must(uuid.NewV4()), RequestedBy: "integration-dispatch",
		DeliveryWorkItemID: &workItemID, CorrelationID: workItemID.String(), Operation: "delivery.implementation",
		InputRef: "private://concurrent-dispatch/" + label, Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
}

func setConcurrentDispatchSearchPath(tx *gorm.DB, schema string) error {
	// schema is generated exclusively from UUID hex characters above.
	return tx.Exec(`SET LOCAL search_path TO "` + schema + `", public`).Error
}

func raceConcurrentDispatchReservations(t *testing.T, db *gorm.DB, schema string, executionID uuid.UUID, requests []concurrentDispatchRequest) []concurrentDispatchResult {
	t.Helper()
	require.Len(t, requests, 2, "this race fixture starts two independent dispatchers")
	start := make(chan struct{})
	ready := make(chan struct{}, len(requests))
	results := make(chan concurrentDispatchResult, len(requests))
	var workers sync.WaitGroup
	for _, request := range requests {
		request := request
		workers.Add(1)
		go func() {
			defer workers.Done()
			ready <- struct{}{}
			<-start
			result := concurrentDispatchResult{request: request}
			result.err = db.Transaction(func(tx *gorm.DB) error {
				if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
					return err
				}
				rows, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, executionID, 2, map[uuid.UUID]uuid.UUID{request.stepID: request.childTaskID}, time.Now().UTC())
				if err != nil {
					return err
				}
				result.rows = rows
				return nil
			})
			results <- result
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	workers.Wait()
	close(results)
	all := make([]concurrentDispatchResult, 0, len(requests))
	for result := range results {
		all = append(all, result)
	}
	return all
}

func assertAllConcurrentDispatchesSucceeded(t *testing.T, results []concurrentDispatchResult) {
	t.Helper()
	require.Len(t, results, 2)
	for _, result := range results {
		require.NoError(t, result.err, "independent ready-step dispatch should commit concurrently")
		require.Len(t, result.rows, 1, "each dispatcher should return exactly its reservation")
	}
}

func concurrentDispatchReadySteps(t *testing.T, db *gorm.DB, schema string, executionID uuid.UUID) []models.DeliveryPlanStep {
	t.Helper()
	var ready []models.DeliveryPlanStep
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
			return err
		}
		_, steps, err := deliveryplansteps.ReadyPlanStepsInTransaction(tx, executionID, time.Now().UTC())
		ready = steps
		return err
	})
	require.NoError(t, err)
	return ready
}

func concurrentDispatchStepIDs(steps []models.DeliveryPlanStep) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(steps))
	for _, step := range steps {
		ids = append(ids, step.ID)
	}
	return ids
}

func reserveConcurrentDispatchDependent(t *testing.T, db *gorm.DB, schema string, executionID, stepID, childTaskID uuid.UUID) error {
	t.Helper()
	_, err := reserveConcurrentDispatchDependentWithRows(t, db, schema, executionID, stepID, childTaskID)
	return err
}

func reserveConcurrentDispatchDependentWithRows(t *testing.T, db *gorm.DB, schema string, executionID, stepID, childTaskID uuid.UUID) ([]models.DeliveryPlanStepAssignment, error) {
	t.Helper()
	var assignments []models.DeliveryPlanStepAssignment
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
			return err
		}
		rows, err := deliveryplansteps.ReserveReadyAssignmentsInTransaction(tx, executionID, 1, map[uuid.UUID]uuid.UUID{stepID: childTaskID}, time.Now().UTC())
		assignments = rows
		return err
	})
	return assignments, err
}

func markConcurrentDispatchRootComplete(t *testing.T, db *gorm.DB, schema string, executionID, stepID uuid.UUID) {
	t.Helper()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
			return err
		}
		assignmentResult := tx.Model(&models.DeliveryPlanStepAssignment{}).
			Where("execution_id = ? AND delivery_plan_step_id = ?", executionID, stepID).
			Updates(map[string]any{"status": models.DeliveryPlanStepAssignmentCompleted, "updated_at": time.Now().UTC()})
		if assignmentResult.Error != nil {
			return assignmentResult.Error
		}
		if assignmentResult.RowsAffected != 1 {
			return fmt.Errorf("complete root assignment: expected one row, got %d", assignmentResult.RowsAffected)
		}
		stepResult := tx.Model(&models.DeliveryPlanStep{}).
			Where("id = ?", stepID).
			Updates(map[string]any{"status": models.DeliveryPlanStepCompleted, "updated_at": time.Now().UTC()})
		if stepResult.Error != nil {
			return stepResult.Error
		}
		if stepResult.RowsAffected != 1 {
			return fmt.Errorf("complete root step: expected one row, got %d", stepResult.RowsAffected)
		}
		return nil
	})
	require.NoError(t, err)
}

func assertConcurrentDispatchRows(t *testing.T, db *gorm.DB, schema string, executionID uuid.UUID, expectedRows int, allowedStepIDs []uuid.UUID) []models.DeliveryPlanStepAssignment {
	t.Helper()
	var assignments []models.DeliveryPlanStepAssignment
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := setConcurrentDispatchSearchPath(tx, schema); err != nil {
			return err
		}
		return tx.Where("execution_id = ?", executionID).Order("delivery_plan_step_id ASC").Find(&assignments).Error
	})
	require.NoError(t, err)
	require.Len(t, assignments, expectedRows, "assignment rows must reflect only the winning reservations")
	allowed := make(map[uuid.UUID]bool, len(allowedStepIDs))
	for _, stepID := range allowedStepIDs {
		allowed[stepID] = true
	}
	seen := make(map[uuid.UUID]bool, len(assignments))
	for _, assignment := range assignments {
		require.True(t, allowed[assignment.DeliveryPlanStepID], "assignment must reference a step in the frozen plan")
		require.False(t, seen[assignment.DeliveryPlanStepID], "one frozen step cannot receive duplicate assignments")
		seen[assignment.DeliveryPlanStepID] = true
	}
	activeCount := 0
	for _, assignment := range assignments {
		if assignment.Status == models.DeliveryPlanStepAssignmentPending || assignment.Status == models.DeliveryPlanStepAssignmentQueued || assignment.Status == models.DeliveryPlanStepAssignmentDispatched || assignment.Status == models.DeliveryPlanStepAssignmentRunning {
			activeCount++
		}
	}
	require.LessOrEqual(t, activeCount, 2, "active reservations must not exceed the frozen execution max_concurrency")
	return assignments
}
