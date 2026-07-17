package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"events-stocks/internal/observability"
	"events-stocks/models"
	"events-stocks/repositories/jobqueuerepository"
	"events-stocks/repositories/outboxrepository"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const analyticsRollupType = "analytics.rollup"
const slackNotificationType = "notification.slack"

type Publisher interface {
	Publish(eventType, body, tenantCode string) error
}

type workerQueuePublisher struct{}

func (workerQueuePublisher) Publish(eventType, body, tenantCode string) error {
	return jobqueuerepository.PublishRawWorkload(body, tenantCode, eventType == slackNotificationType)
}

func EnqueueAnalyticsRollup(ctx context.Context, db *gorm.DB, eventID uuid.UUID, tenantCode string) (bool, error) {
	if db == nil || !jobqueuerepository.IsConfigured() {
		return false, nil
	}
	correlationID := observability.CorrelationID(ctx)
	body, dedupeKey, normalizedTenant, err := jobqueuerepository.BuildAnalyticsRollupMessage(eventID, tenantCode, time.Now().UTC(), correlationID)
	if err != nil {
		return false, err
	}
	return outboxrepository.Enqueue(db, &models.OutboxEvent{
		EventType:     analyticsRollupType,
		DedupeKey:     dedupeKey,
		TenantCode:    normalizedTenant,
		CorrelationID: correlationID,
		Payload:       body,
		State:         outboxrepository.StatePending,
		AvailableAt:   time.Now().UTC(),
	})
}

func StartDispatcher(ctx context.Context, db *gorm.DB) {
	go runDispatcher(ctx, db, workerQueuePublisher{}, 2*time.Second)
}

func runDispatcher(ctx context.Context, db *gorm.DB, publisher Publisher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	cleanupTicker := time.NewTicker(time.Hour)
	defer cleanupTicker.Stop()
	dispatchOnce(ctx, db, publisher)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatchOnce(ctx, db, publisher)
		case <-cleanupTicker.C:
			if err := outboxrepository.DeleteCompletedBefore(db, time.Now().UTC().Add(-7*24*time.Hour)); err != nil {
				slog.Warn("outbox cleanup failed", "error", err)
			}
		}
	}
}

func dispatchOnce(ctx context.Context, db *gorm.DB, publisher Publisher) {
	if ctx.Err() != nil || db == nil {
		return
	}
	events, err := outboxrepository.ClaimBatch(db, 20, 30*time.Second)
	if err != nil {
		slog.Error("outbox claim failed", "error", err)
		return
	}
	for _, event := range events {
		if ctx.Err() != nil {
			return
		}
		if event.EventType != analyticsRollupType && event.EventType != slackNotificationType {
			err = fmt.Errorf("unsupported outbox event type %s", event.EventType)
		} else {
			err = publisher.Publish(event.EventType, event.Payload, event.TenantCode)
		}
		if err != nil {
			slog.Warn("outbox delivery failed", "event", "outbox_delivery_failed", "component", "outbox", "outbox_id", event.ID, "job_id", event.DedupeKey, "correlation_id", event.CorrelationID, "application", event.TenantCode, "job_type", event.EventType, "attempt", event.Attempts, "error", err)
			_ = outboxrepository.ScheduleRetry(db, event, err)
			continue
		}
		if markErr := outboxrepository.MarkCompleted(db, event.ID); markErr != nil {
			slog.Error("outbox completion failed", "event", "outbox_completion_failed", "component", "outbox", "outbox_id", event.ID, "job_id", event.DedupeKey, "correlation_id", event.CorrelationID, "error", markErr)
		} else {
			slog.Info("outbox delivery completed", "event", "outbox_delivery_completed", "component", "outbox", "outbox_id", event.ID, "job_id", event.DedupeKey, "correlation_id", event.CorrelationID, "application", event.TenantCode, "job_type", event.EventType, "attempt", event.Attempts)
		}
	}
}
