package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	deliveryScheduleDispatchInterval = 15 * time.Second
	deliveryScheduleDispatchBatch    = 16
)

// StartDeliveryScheduleDispatcher starts one bounded, database-only tick loop.
// Multiple API replicas may run it safely because each schedule is claimed via
// SKIP LOCKED and occurrence/work-item creation is transactional.
func StartDeliveryScheduleDispatcher(ctx context.Context, db *gorm.DB) {
	if ctx == nil || db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(deliveryScheduleDispatchInterval)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if err := DispatchDeliverySchedulesOnce(ctx, db, time.Now().UTC()); err != nil && ctx.Err() == nil {
				slog.Warn("delivery schedule dispatcher unavailable", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// DispatchDeliverySchedulesOnce materializes at most one overdue occurrence
// per schedule and per tick. A missed interval is coalesced: the oldest stored
// due slot yields one planning item, and the next slot advances strictly past
// now. It does not enqueue a provider task or bypass the normal continuation
// dispatcher.
func DispatchDeliverySchedulesOnce(ctx context.Context, db *gorm.DB, now time.Time) error {
	if db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	var scheduleIDs []uuid.UUID
	if err := db.WithContext(ctx).Model(&models.DeliveryAutomationSchedule{}).
		Where("status = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", models.DeliveryAutomationScheduleActive, now).
		Order("next_run_at ASC, id ASC").Limit(deliveryScheduleDispatchBatch).
		Pluck("id", &scheduleIDs).Error; err != nil {
		return err
	}
	for _, scheduleID := range scheduleIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := materializeDeliveryScheduleOccurrence(ctx, db, scheduleID, now); err != nil {
			return err
		}
	}
	return nil
}

func materializeDeliveryScheduleOccurrence(ctx context.Context, db *gorm.DB, scheduleID uuid.UUID, now time.Time) error {
	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var schedule models.DeliveryAutomationSchedule
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("id = ? AND status = ? AND next_run_at IS NOT NULL AND next_run_at <= ?", scheduleID, models.DeliveryAutomationScheduleActive, now).
			First(&schedule).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}

		dueAt := schedule.NextRunAt.UTC()
		localOccurrence := dueAt.Format(time.RFC3339)
		if location, err := time.LoadLocation(schedule.TimeZone); err == nil {
			localOccurrence = dueAt.In(location).Format("2006-01-02T15:04")
		}
		key := deliveryScheduleOccurrenceKey(schedule.ID, schedule.Revision, localOccurrence, schedule.TimeZone)
		var existing models.DeliveryAutomationScheduleOccurrence
		existingErr := tx.Where("schedule_id = ? AND schedule_revision = ? AND occurrence_key = ?", schedule.ID, schedule.Revision, key).First(&existing).Error
		if existingErr == nil {
			// Defensive repair for rows created by an older non-atomic build.
			// Current writer commits occurrence and schedule advancement together.
			return advanceDeliveryScheduleAfterOccurrence(tx, &schedule, now, existing)
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		var template deliveryScheduleTemplate
		if err := json.Unmarshal([]byte(schedule.TemplateJSON), &template); err != nil {
			return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "template_invalid", now)
		}
		recurrence := deliveryScheduleRecurrence{}
		if err := json.Unmarshal([]byte(schedule.RecurrenceJSON), &recurrence); err != nil {
			return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "recurrence_invalid", now)
		}
		recurrence, _, recurrenceErr := normalizeDeliveryScheduleRecurrence(recurrence)
		if recurrenceErr != nil || recurrence.TimeZone != schedule.TimeZone {
			return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "recurrence_invalid", now)
		}
		input, normalizeErr := normalizeScheduleOccurrenceTemplate(schedule, template)
		if normalizeErr != nil {
			return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "template_invalid", now)
		}
		if err := validateDeliveryScheduleTemplateSources(tx, schedule.ProjectID, template, input); err != nil {
			if errors.Is(err, errWorkItemContextNotReady) {
				return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "context_not_ready", now)
			}
			if errors.Is(err, errInvalidTaskPrimaryRepository) {
				return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "primary_repository_invalid", now)
			}
			return err
		}
		var project models.DeliveryProject
		if err := tx.Select("id").First(&project, "id = ?", schedule.ProjectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, "project_unavailable", now)
			}
			return err
		}

		const materializationSavepoint = "delivery_schedule_work_item"
		if err := tx.SavePoint(materializationSavepoint).Error; err != nil {
			return err
		}
		item, err := createWorkItemInTransaction(tx, input, now)
		if err != nil {
			if errors.Is(err, errWorkItemContextNotReady) || errors.Is(err, errInvalidTaskPrimaryRepository) {
				// Undo any partial work item/snapshots while retaining the locked
				// schedule row, then durably record the blocked occurrence in this
				// same transaction.
				if rollbackErr := tx.RollbackTo(materializationSavepoint).Error; rollbackErr != nil {
					return rollbackErr
				}
				return blockDeliveryScheduleOccurrence(tx, &schedule, dueAt, localOccurrence, key, scheduleFailureCode(err), now)
			}
			return err
		}
		materializedAt := now
		occurrence := models.DeliveryAutomationScheduleOccurrence{
			ScheduleID: schedule.ID, ScheduleRevision: schedule.Revision, OccurrenceKey: key,
			ScheduledFor: dueAt, LocalOccurrence: localOccurrence, TimeZone: schedule.TimeZone,
			TemplateJSON: schedule.TemplateJSON, Status: models.DeliveryAutomationScheduleOccurrenceMaterialized,
			WorkItemID: &item.ID, CreatedAt: now, MaterializedAt: &materializedAt,
		}
		if err := tx.Create(&occurrence).Error; err != nil {
			return err
		}
		if err := appendDeliveryScheduleEvent(tx, schedule.ID, &occurrence.ID, &item.ID, "occurrence_materialized", "", map[string]any{
			"revision": schedule.Revision, "status": models.DeliveryAutomationScheduleOccurrenceMaterialized,
		}, now); err != nil {
			return err
		}
		return advanceDeliveryScheduleAfterOccurrence(tx, &schedule, now, occurrence)
	})
	return txErr
}

func normalizeScheduleOccurrenceTemplate(schedule models.DeliveryAutomationSchedule, template deliveryScheduleTemplate) (normalizedWorkItemRequest, error) {
	_, input, err := normalizeDeliveryScheduleTemplate(schedule.ProjectID, schedule.CreatedBy, template)
	if err != nil {
		return normalizedWorkItemRequest{}, err
	}
	return input, nil
}

func blockDeliveryScheduleOccurrence(tx *gorm.DB, schedule *models.DeliveryAutomationSchedule, dueAt time.Time, localOccurrence, key, failureCode string, now time.Time) error {
	occurrence := models.DeliveryAutomationScheduleOccurrence{
		ScheduleID: schedule.ID, ScheduleRevision: schedule.Revision, OccurrenceKey: key,
		ScheduledFor: dueAt, LocalOccurrence: localOccurrence, TimeZone: schedule.TimeZone,
		// A blocked occurrence did not materialize a work item, so it needs no
		// duplicate template snapshot. Keeping this empty also avoids retaining
		// any unexpected fields if the stored schedule row is corrupt.
		TemplateJSON: "{}", Status: models.DeliveryAutomationScheduleOccurrenceBlocked,
		FailureCode: failureCode, CreatedAt: now,
	}
	if err := tx.Create(&occurrence).Error; err != nil {
		return err
	}
	schedule.Status = models.DeliveryAutomationSchedulePaused
	schedule.PausedAt = &now
	schedule.NextRunAt = nil
	schedule.UpdatedAt = now
	if err := tx.Save(schedule).Error; err != nil {
		return err
	}
	return appendDeliveryScheduleEvent(tx, schedule.ID, &occurrence.ID, nil, "occurrence_blocked", "", map[string]any{
		"revision": schedule.Revision, "status": models.DeliveryAutomationScheduleOccurrenceBlocked, "failure_code": failureCode,
	}, now)
}

func advanceDeliveryScheduleAfterOccurrence(tx *gorm.DB, schedule *models.DeliveryAutomationSchedule, now time.Time, occurrence models.DeliveryAutomationScheduleOccurrence) error {
	recurrence := deliveryScheduleRecurrence{}
	if err := json.Unmarshal([]byte(schedule.RecurrenceJSON), &recurrence); err != nil {
		return err
	}
	nextRun, _, err := nextDeliveryScheduleOccurrence(recurrence, now)
	if err != nil {
		return err
	}
	schedule.LastRunAt = &now
	schedule.UpdatedAt = now
	if nextRun == nil {
		schedule.Status = models.DeliveryAutomationScheduleEnded
		schedule.NextRunAt = nil
	} else {
		schedule.NextRunAt = nextRun
	}
	if err := tx.Save(schedule).Error; err != nil {
		return err
	}
	if schedule.Status == models.DeliveryAutomationScheduleEnded {
		return appendDeliveryScheduleEvent(tx, schedule.ID, &occurrence.ID, occurrence.WorkItemID, "ended", "", map[string]any{"revision": schedule.Revision, "status": schedule.Status}, now)
	}
	return nil
}

func scheduleFailureCode(err error) string {
	switch {
	case errors.Is(err, errWorkItemContextNotReady):
		return "context_not_ready"
	case errors.Is(err, errInvalidTaskPrimaryRepository):
		return "primary_repository_invalid"
	default:
		return "template_invalid"
	}
}
