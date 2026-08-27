package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"events-stocks/internal/observability"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/repositories/jobqueuerepository"
	"events-stocks/repositories/outboxrepository"
	sqsrepository "events-stocks/repositories/sqsrepository"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type Publisher interface {
	Publish(eventType, body, tenantCode string) error
}

type workerQueuePublisher struct{}

func (workerQueuePublisher) Publish(eventType, body, tenantCode string) error {
	route, err := RouteFor(eventType)
	if err != nil {
		return err
	}
	if err := ValidateRoute(route, tenantCode); err != nil {
		return err
	}
	switch route.Runtime {
	case RuntimeLocalAgent:
		return automationqueue.PublishSerialized(body)
	case RuntimeRustWorker:
		return jobqueuerepository.PublishRawWorkload(body, tenantCode, route.NotificationLane)
	case RuntimeMediaLambda:
		return sqsrepository.PublishSerialized(body)
	default:
		return fmt.Errorf("outbox route %s targets unsupported runtime %s", route.EventType, route.Runtime)
	}
}

// EnqueueAutomationProcess durably couples a task to its local-agent handoff.
// Callers must provide the transaction that created the task, so a task can
// never commit without a retryable delivery record.
func EnqueueAutomationProcess(ctx context.Context, db *gorm.DB, message automationqueue.Message) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("outbox database is unavailable")
	}
	if err := automationqueue.Validate(message); err != nil {
		return false, err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return false, fmt.Errorf("marshal automation outbox message: %w", err)
	}
	return outboxrepository.Enqueue(db, &models.OutboxEvent{
		EventType:     automationProcessType,
		DedupeKey:     message.JobID,
		TenantCode:    "itbem",
		CorrelationID: observability.NormalizeCorrelationID(message.CorrelationID),
		Payload:       string(body),
		State:         outboxrepository.StatePending,
		AvailableAt:   time.Now().UTC(),
	})
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
		err = publisher.Publish(event.EventType, event.Payload, event.TenantCode)
		if err != nil {
			slog.Warn("outbox delivery failed", "event", "outbox_delivery_failed", "component", "outbox", "outbox_id", event.ID, "job_id", event.DedupeKey, "correlation_id", event.CorrelationID, "application", event.TenantCode, "job_type", event.EventType, "target_runtime", event.TargetRuntime, "queue_namespace", event.QueueNamespace, "attempt", event.Attempts, "error", err)
			_ = outboxrepository.ScheduleRetry(db, event, err)
			continue
		}
		if markErr := outboxrepository.MarkCompleted(db, event.ID); markErr != nil {
			slog.Error("outbox completion failed", "event", "outbox_completion_failed", "component", "outbox", "outbox_id", event.ID, "job_id", event.DedupeKey, "correlation_id", event.CorrelationID, "error", markErr)
		} else {
			slog.Info("outbox delivery completed", "event", "outbox_delivery_completed", "component", "outbox", "outbox_id", event.ID, "job_id", event.DedupeKey, "correlation_id", event.CorrelationID, "application", event.TenantCode, "job_type", event.EventType, "target_runtime", event.TargetRuntime, "queue_namespace", event.QueueNamespace, "attempt", event.Attempts)
		}
	}
}
