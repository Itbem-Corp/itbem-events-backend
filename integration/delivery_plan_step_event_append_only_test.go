//go:build integration

package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeliveryPlanStepEventsAreAppendOnly(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			rollbackFixture := errors.New("rollback append-only fixture")
			var mutationErr error
			err := configuration.DB.Transaction(func(tx *gorm.DB) error {
				event := createStepEventFixture(t, tx)
				switch operation {
				case "update":
					mutationErr = tx.Model(&models.DeliveryPlanStepEvent{}).Where("id = ?", event.ID).Update("summary", "rewritten").Error
				case "delete":
					mutationErr = tx.Delete(&event).Error
				}
				return rollbackFixture
			})
			require.ErrorIs(t, err, rollbackFixture, "the fixture transaction must roll back")
			require.Error(t, mutationErr)
			require.Contains(t, strings.ToLower(mutationErr.Error()), "delivery plan step events are append-only")
		})
	}
}

func createStepEventFixture(t *testing.T, tx *gorm.DB) models.DeliveryPlanStepEvent {
	t.Helper()
	suffix := uuid.Must(uuid.NewV4()).String()[:8]
	clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Append-only test " + suffix, Code: "STEP_EVENT_" + suffix, Level: 10, IsActive: true}
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Append-only test " + suffix, Code: "step-event-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Step event test " + suffix, Slug: "step-event-test-" + suffix, Status: "active", CreatedBy: "integration"}
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, RequestedBy: "integration", Title: "Append-only step event fixture", State: "planning"}
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Version: 1, Status: "approved", Summary: "fixture", StructuredJSON: `{}`, CreatedAt: time.Now().UTC()}
	step := models.DeliveryPlanStep{ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepKey: "fixture", IdempotencyKey: "fixture-" + suffix, DisplayOrder: 1, Title: "Fixture", AcceptanceCriteriaJSON: `[]`, Status: models.DeliveryPlanStepPlanned}
	for _, value := range []any{&clientType, &client, &project, &item, &plan, &step} {
		require.NoError(t, tx.Create(value).Error)
	}
	event := models.DeliveryPlanStepEvent{
		ID: uuid.Must(uuid.NewV4()), PlanID: plan.ID, StepID: step.ID,
		EventType: models.DeliveryPlanStepEventReady, ToStatus: models.DeliveryPlanStepReady,
		AutomationTaskID: uuid.Must(uuid.NewV4()), Summary: "ready", OccurredAt: time.Now().UTC(),
	}
	require.NoError(t, tx.Create(&event).Error)
	return event
}
