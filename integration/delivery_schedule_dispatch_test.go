//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"events-stocks/configuration"
	delivery "events-stocks/controllers/delivery"
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// TestDeliveryScheduleDispatchMaterializesOnePlanningItem exercises the real
// PostgreSQL dispatcher inside a transaction that is deliberately rolled
// back. It uses no provider, queue, or external service and leaves append-only
// history untouched after the test.
func TestDeliveryScheduleDispatchMaterializesOnePlanningItem(t *testing.T) {
	db := configuration.DB
	if db == nil {
		t.Fatal("integration TestMain did not provide its disposable PostgreSQL database")
	}

	intentionalRollback := errors.New("rollback delivery schedule dispatch fixture")
	caseErr := db.Transaction(func(tx *gorm.DB) error {
		suffix := uuid.Must(uuid.NewV4()).String()[:8]
		actor := "integration-schedule-" + suffix
		clientType := models.ClientType{ID: uuid.Must(uuid.NewV4()), Name: "Schedule test " + suffix, Code: "SCHEDULE_" + suffix, Level: 10, IsActive: true}
		client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Schedule test " + suffix, Code: "schedule-test-" + suffix, ClientTypeID: clientType.ID, IsActive: true}
		project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "Schedule test " + suffix, Slug: "schedule-test-" + suffix, Summary: "integration fixture", Status: "active", CreatedBy: actor}
		contextSource := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Kind: "document", Name: "Schedule fixture context", Reference: "document://schedule-fixture", Revision: "r1", Status: "ready", MetadataJSON: `{}`}
		for _, fixture := range []any{&clientType, &client, &project, &contextSource} {
			if err := tx.Create(fixture).Error; err != nil {
				return fmt.Errorf("create fixture %T: %w", fixture, err)
			}
		}

		now := time.Now().UTC().Truncate(time.Second)
		dueAt := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if !dueAt.Before(now) {
			dueAt = dueAt.AddDate(0, 0, -1)
		}
		startDate := dueAt.AddDate(0, 0, -7).Format("2006-01-02")
		templateJSON, err := json.Marshal(map[string]any{
			"title": "Recurring planning test", "description": "Synthetic integration fixture",
			"expected_outcome": "A bounded plan is proposed", "context_source_ids": []string{contextSource.ID.String()},
			"budget_microusd": int64(250_000), "budget_alert_percent": 80,
		})
		if err != nil {
			return fmt.Errorf("encode template: %w", err)
		}
		recurrenceJSON, err := json.Marshal(map[string]any{
			"frequency": "daily", "interval": 1, "local_time": "00:00", "time_zone": "UTC",
			"starts_on": startDate, "misfire_policy": "coalesce",
		})
		if err != nil {
			return fmt.Errorf("encode recurrence: %w", err)
		}
		schedule := models.DeliveryAutomationSchedule{
			ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Name: "Integration daily planning", Status: models.DeliveryAutomationScheduleActive,
			Revision: 1, TemplateJSON: string(templateJSON), RecurrenceJSON: string(recurrenceJSON), TimeZone: "UTC",
			NextRunAt: &dueAt, CreatedBy: actor, UpdatedBy: actor,
		}
		if err := tx.Create(&schedule).Error; err != nil {
			return fmt.Errorf("create schedule: %w", err)
		}

		if err := delivery.DispatchDeliverySchedulesOnce(context.Background(), tx, now); err != nil {
			return fmt.Errorf("first schedule dispatch: %w", err)
		}
		var occurrences []models.DeliveryAutomationScheduleOccurrence
		if err := tx.Where("schedule_id = ?", schedule.ID).Find(&occurrences).Error; err != nil {
			return fmt.Errorf("load first occurrence: %w", err)
		}
		if len(occurrences) != 1 || occurrences[0].Status != models.DeliveryAutomationScheduleOccurrenceMaterialized || occurrences[0].WorkItemID == nil {
			return fmt.Errorf("first dispatch should materialize exactly one occurrence with a work item; got %#v", occurrences)
		}
		workItemID := *occurrences[0].WorkItemID
		var item models.DeliveryWorkItem
		if err := tx.First(&item, "id = ?", workItemID).Error; err != nil {
			return fmt.Errorf("load work item: %w", err)
		}
		if item.ProjectID != project.ID || item.State != deliveryworkflow.StatePlanning || item.BudgetMicros <= 0 {
			return fmt.Errorf("materialized work item escaped the project/planning/budget contract: project=%s state=%q budget=%d", item.ProjectID, item.State, item.BudgetMicros)
		}
		var continuations []models.DeliveryContinuation
		if err := tx.Where("work_item_id = ?", workItemID).Find(&continuations).Error; err != nil {
			return fmt.Errorf("load continuation: %w", err)
		}
		if len(continuations) != 1 || continuations[0].Phase != "plan" || continuations[0].Status != "pending" {
			return fmt.Errorf("expected exactly one pending plan continuation; got %#v", continuations)
		}
		if err := assertNoScheduleDispatchBypass(tx, workItemID); err != nil {
			return err
		}

		// Simulate a stale due timestamp after a successful commit. The durable
		// occurrence key must repair schedule advancement without creating a
		// second work item, continuation, or provider task.
		if err := tx.Model(&models.DeliveryAutomationSchedule{}).Where("id = ?", schedule.ID).Update("next_run_at", dueAt).Error; err != nil {
			return fmt.Errorf("restore due timestamp for idempotency check: %w", err)
		}
		if err := delivery.DispatchDeliverySchedulesOnce(context.Background(), tx, now); err != nil {
			return fmt.Errorf("second schedule dispatch: %w", err)
		}
		var occurrenceCount, workItemCount, continuationCount int64
		if err := tx.Model(&models.DeliveryAutomationScheduleOccurrence{}).Where("schedule_id = ?", schedule.ID).Count(&occurrenceCount).Error; err != nil {
			return fmt.Errorf("count occurrences: %w", err)
		}
		if err := tx.Model(&models.DeliveryWorkItem{}).Where("project_id = ? AND id = ?", project.ID, workItemID).Count(&workItemCount).Error; err != nil {
			return fmt.Errorf("count work item: %w", err)
		}
		if err := tx.Model(&models.DeliveryContinuation{}).Where("work_item_id = ?", workItemID).Count(&continuationCount).Error; err != nil {
			return fmt.Errorf("count continuations: %w", err)
		}
		if occurrenceCount != 1 || workItemCount != 1 || continuationCount != 1 {
			return fmt.Errorf("idempotent second pass duplicated materialization: occurrences=%d work_items=%d continuations=%d", occurrenceCount, workItemCount, continuationCount)
		}
		if err := assertNoScheduleDispatchBypass(tx, workItemID); err != nil {
			return err
		}
		return intentionalRollback
	})
	if !errors.Is(caseErr, intentionalRollback) {
		t.Fatalf("schedule dispatcher round-trip failed (fixture transaction rolled back): %v", caseErr)
	}
}

func assertNoScheduleDispatchBypass(tx *gorm.DB, workItemID uuid.UUID) error {
	var taskCount, planCount, gateCount int64
	checks := []struct {
		model  any
		label  string
		column string
		count  *int64
	}{
		{&models.AutomationTask{}, "automation tasks", "delivery_work_item_id", &taskCount},
		{&models.DeliveryPlan{}, "delivery plans", "work_item_id", &planCount},
		{&models.DeliveryGate{}, "delivery gates", "work_item_id", &gateCount},
	}
	for _, check := range checks {
		if err := tx.Model(check.model).Where(check.column+" = ?", workItemID).Count(check.count).Error; err != nil {
			return fmt.Errorf("count %s: %w", check.label, err)
		}
	}
	if taskCount != 0 || planCount != 0 || gateCount != 0 {
		return fmt.Errorf("schedule bypassed the plan/gate flow: automation_tasks=%d plans=%d gates=%d", taskCount, planCount, gateCount)
	}
	return nil
}
