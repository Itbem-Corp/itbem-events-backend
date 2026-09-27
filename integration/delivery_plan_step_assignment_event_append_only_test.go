//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeliveryPlanStepAssignmentEventsCaptureDirectUpdatesAndAreAppendOnly(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")
	// Exercise the actual startup migration path and its idempotence. All
	// records below live only in TestMain's disposable database.
	require.NoError(t, configuration.MigrateModelsForTest(db))
	require.NoError(t, configuration.MigrateModelsForTest(db))

	var assignment models.DeliveryPlanStepAssignment
	var plan models.DeliveryPlan
	var parentTaskID, childTaskID uuid.UUID
	now := time.Now().UTC().Truncate(time.Microsecond)
	firstMachineID, secondMachineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		stepEvent := createStepEventFixture(t, tx)
		if err := tx.First(&plan, "id = ?", stepEvent.PlanID).Error; err != nil {
			return err
		}
		parentTaskID, childTaskID = uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
		gate := models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: plan.WorkItemID, Kind: "plan_approval", Decision: "approved", DecidedBy: "integration", DecidedAt: now, CreatedAt: now}
		if err := tx.Create(&gate).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DeliveryPlan{}).Where("id = ?", plan.ID).Update("approved_gate_id", gate.ID).Error; err != nil {
			return err
		}
		workItemID := plan.WorkItemID
		parentTask := models.AutomationTask{ID: parentTaskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: "integration", DeliveryWorkItemID: &workItemID, CorrelationID: workItemID.String(), Operation: "delivery.implementation", InputRef: "private://assignment-event-parent", Status: "queued", CreatedAt: now, UpdatedAt: now}
		childTask := models.AutomationTask{ID: childTaskID, JobID: uuid.Must(uuid.NewV4()), RequestedBy: "integration", DeliveryWorkItemID: &workItemID, CorrelationID: workItemID.String(), Operation: "delivery.implementation", InputRef: "private://assignment-event-child", Status: "queued", CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&parentTask).Error; err != nil {
			return err
		}
		if err := tx.Create(&childTask).Error; err != nil {
			return err
		}
		execution := models.DeliveryPlanExecution{
			ID: uuid.Must(uuid.NewV4()), AutomationTaskID: parentTaskID, IdempotencyKey: "assignment-event-" + uuid.Must(uuid.NewV4()).String(),
			PlanID: plan.ID, PlanVersion: plan.Version, ApprovedGateID: gate.ID, PlanHash: strings.Repeat("a", 64), MaxConcurrency: 2,
			Status: models.DeliveryPlanExecutionRunning, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&execution).Error; err != nil {
			return err
		}
		assignment = models.DeliveryPlanStepAssignment{
			ID: uuid.Must(uuid.NewV4()), ExecutionID: execution.ID, DeliveryPlanStepID: stepEvent.StepID, ChildAutomationTaskID: childTaskID,
			TargetMachineID: firstMachineID.String(), TargetAgentKey: "generalist", Status: models.DeliveryPlanStepAssignmentQueued,
			CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&assignment).Error
	}))

	var events []models.DeliveryPlanStepAssignmentEvent
	require.NoError(t, db.Where("assignment_id = ?", assignment.ID).Order("occurred_at ASC, id ASC").Find(&events).Error)
	require.Len(t, events, 1, "assignment insertion should be captured by PostgreSQL")
	created := events[0]
	require.Equal(t, models.DeliveryPlanStepAssignmentEventCreated, created.EventType)
	require.Equal(t, plan.ID, created.PlanID)
	require.Equal(t, plan.Version, created.PlanVersion)
	require.Equal(t, assignment.ExecutionID, created.ExecutionID)
	require.Equal(t, assignment.DeliveryPlanStepID, created.StepID)
	require.Equal(t, parentTaskID, created.ParentAutomationTaskID)
	require.Equal(t, childTaskID, created.ChildAutomationTaskID)
	require.Equal(t, models.DeliveryPlanStepAssignmentQueued, created.Status)
	require.Equal(t, "generalist", created.TargetAgentKey)
	require.Equal(t, firstMachineID.String(), created.TargetMachineID)

	// These map/column updates bypass GORM hooks and exercise the database
	// trigger directly. One transition changes status, the next reassigns the
	// target; writing an identical value must not manufacture another event.
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", assignment.ID).UpdateColumn("status", models.DeliveryPlanStepAssignmentDispatched).Error)
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", assignment.ID).Updates(map[string]any{
		"target_agent_key": "reviewer", "target_machine_id": secondMachineID.String(),
	}).Error)
	require.NoError(t, db.Model(&models.DeliveryPlanStepAssignment{}).Where("id = ?", assignment.ID).UpdateColumn("target_agent_key", "reviewer").Error)

	events = nil
	require.NoError(t, db.Where("assignment_id = ?", assignment.ID).Order("occurred_at ASC, id ASC").Find(&events).Error)
	require.Len(t, events, 3, "each real status/target transition should be captured exactly once")
	require.Equal(t, models.DeliveryPlanStepAssignmentEventStatusChanged, events[1].EventType)
	require.Equal(t, models.DeliveryPlanStepAssignmentQueued, events[1].PreviousStatus)
	require.Equal(t, models.DeliveryPlanStepAssignmentDispatched, events[1].Status)
	require.Equal(t, "generalist", events[1].TargetAgentKey)
	require.Equal(t, models.DeliveryPlanStepAssignmentEventTargetChanged, events[2].EventType)
	require.Equal(t, models.DeliveryPlanStepAssignmentDispatched, events[2].PreviousStatus)
	require.Equal(t, "generalist", events[2].PreviousTargetAgentKey)
	require.Equal(t, "reviewer", events[2].TargetAgentKey)
	require.Equal(t, firstMachineID.String(), events[2].PreviousTargetMachineID)
	require.Equal(t, secondMachineID.String(), events[2].TargetMachineID)

	updateErr := db.Model(&models.DeliveryPlanStepAssignmentEvent{}).Where("id = ?", events[0].ID).UpdateColumn("status", "rewritten").Error
	require.Error(t, updateErr)
	require.Contains(t, strings.ToLower(updateErr.Error()), "delivery plan step assignment events are append-only")
	deleteErr := db.Where("id = ?", events[0].ID).Delete(&models.DeliveryPlanStepAssignmentEvent{}).Error
	require.Error(t, deleteErr)
	require.Contains(t, strings.ToLower(deleteErr.Error()), "delivery plan step assignment events are append-only")
}
