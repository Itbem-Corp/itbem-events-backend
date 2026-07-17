package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/observability"
	"events-stocks/models"
	"events-stocks/repositories/jobqueuerepository"
	"events-stocks/repositories/outboxrepository"

	"github.com/gofrs/uuid"
)

var ErrNotConfigured = errors.New("worker notification queue is not configured")

const slackNotificationType = "notification.slack"

type slackEnvelope struct {
	SchemaVersion int                    `json:"schema_version"`
	JobID         uuid.UUID              `json:"job_id"`
	OccurredAt    time.Time              `json:"occurred_at"`
	CorrelationID string                 `json:"correlation_id,omitempty"`
	Type          string                 `json:"type"`
	Payload       dtos.SlackNotification `json:"payload"`
}

// Send durably hands a presentation-independent notification to the Rust
// workers. The API has no Slack webhook, SSM access, or HTTP transport.
func Send(ctx context.Context, notification dtos.SlackNotification) error {
	if !jobqueuerepository.IsConfigured() || configuration.DB == nil {
		return ErrNotConfigured
	}
	if strings.TrimSpace(notification.Title) == "" || strings.TrimSpace(notification.Summary) == "" {
		return errors.New("slack notification title and summary are required")
	}
	now := time.Now().UTC()
	jobID := uuid.Must(uuid.NewV4())
	correlationID := observability.CorrelationID(ctx)
	envelope := slackEnvelope{SchemaVersion: 2, JobID: jobID, OccurredAt: now, CorrelationID: correlationID, Type: slackNotificationType, Payload: notification}
	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal Slack notification job: %w", err)
	}
	inserted, err := outboxrepository.Enqueue(configuration.DB, &models.OutboxEvent{
		EventType: slackNotificationType, DedupeKey: jobID.String(), TenantCode: "eventiapp",
		CorrelationID: correlationID, Payload: string(body), State: outboxrepository.StatePending, AvailableAt: now,
	})
	if err != nil {
		return fmt.Errorf("enqueue Slack notification: %w", err)
	}
	if !inserted {
		return errors.New("slack notification was already queued")
	}
	return nil
}
