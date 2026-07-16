// Package jobqueuerepository publishes recomputable business/data jobs for
// itbem-events-workers. Media jobs deliberately remain in sqsrepository.
package jobqueuerepository

import (
	"context"
	"encoding/json"
	"events-stocks/configuration"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/gofrs/uuid"
)

const (
	jobSchemaVersion     = 1
	analyticsJobType     = "analytics.rollup"
	analyticsJobBucket   = time.Minute
	performanceJobType   = "performance.rollup"
	performanceJobBucket = 5 * time.Minute
	publishTimeout       = 5 * time.Second
)

type analyticsRollupPayload struct {
	EventID     uuid.UUID `json:"event_id"`
	RequestedAt time.Time `json:"requested_at"`
	Trigger     string    `json:"trigger"`
}

type analyticsRollupEnvelope struct {
	SchemaVersion int                    `json:"schema_version"`
	JobID         uuid.UUID              `json:"job_id"`
	OccurredAt    time.Time              `json:"occurred_at"`
	Type          string                 `json:"type"`
	Payload       analyticsRollupPayload `json:"payload"`
}

type performanceRollupPayload struct {
	RequestedAt     time.Time `json:"requested_at"`
	LookbackMinutes int       `json:"lookback_minutes"`
	Trigger         string    `json:"trigger"`
}

type performanceRollupEnvelope struct {
	SchemaVersion int                      `json:"schema_version"`
	JobID         uuid.UUID                `json:"job_id"`
	OccurredAt    time.Time                `json:"occurred_at"`
	Type          string                   `json:"type"`
	Payload       performanceRollupPayload `json:"payload"`
}

var (
	client                     *sqs.Client
	queueURL                   string
	once                       sync.Once
	publishMu                  sync.Mutex
	publishedBuckets           = map[uuid.UUID]time.Time{}
	performancePublishedBucket time.Time
)

// Init is fail-open so local/API operation remains available without workers.
// Analytics reads retain their synchronous fallback until a snapshot exists.
func Init(region, accessKeyID, secretAccessKey, workerQueueURL string) {
	once.Do(func() {
		queueURL = strings.TrimSpace(workerQueueURL)
		if queueURL == "" {
			slog.Info("jobqueuerepository: worker queue not configured — derived jobs disabled")
			return
		}
		cfg, err := configuration.LoadAWSConfig(context.Background(), region, accessKeyID, secretAccessKey)
		if err != nil {
			slog.Error("jobqueuerepository: failed to load AWS config", "error", err)
			return
		}
		client = sqs.NewFromConfig(cfg)
		slog.Info("jobqueuerepository: worker queue publisher initialized")
	})
}

// PublishPerformanceRollup emits at most one global job per five-minute
// window. The payload contains no event or visitor identifier.
func PublishPerformanceRollup() (bool, error) {
	if client == nil || queueURL == "" {
		return false, nil
	}
	now := time.Now().UTC()
	bucket := now.Truncate(performanceJobBucket)
	publishMu.Lock()
	if performancePublishedBucket.Equal(bucket) {
		publishMu.Unlock()
		return false, nil
	}
	performancePublishedBucket = bucket
	publishMu.Unlock()
	release := func() {
		publishMu.Lock()
		if performancePublishedBucket.Equal(bucket) {
			performancePublishedBucket = time.Time{}
		}
		publishMu.Unlock()
	}
	envelope := performanceRollupEnvelope{
		SchemaVersion: jobSchemaVersion,
		JobID:         uuid.NewV5(uuid.NamespaceURL, fmt.Sprintf("eventiapp:%s:%s", performanceJobType, bucket.Format(time.RFC3339))),
		OccurredAt:    now,
		Type:          performanceJobType,
		Payload:       performanceRollupPayload{RequestedAt: now, LookbackMinutes: 15, Trigger: "rum_sample"},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		release()
		return false, fmt.Errorf("marshal performance rollup job: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(body))})
	if err != nil {
		release()
		return false, fmt.Errorf("publish performance rollup job: %w", err)
	}
	return true, nil
}

// PublishAnalyticsRollup publishes a deterministic per-event/per-minute job.
// Repeated stale reads in the same minute share a persistent idempotency key.
func PublishAnalyticsRollup(eventID uuid.UUID) (bool, error) {
	if client == nil || queueURL == "" || eventID == uuid.Nil {
		return false, nil
	}
	envelope := buildAnalyticsRollupEnvelope(eventID, time.Now().UTC())
	bucket := envelope.OccurredAt.Truncate(analyticsJobBucket)
	if !reserveAnalyticsPublish(eventID, bucket) {
		return false, nil
	}
	releaseReservation := func() {
		publishMu.Lock()
		if publishedBuckets[eventID].Equal(bucket) {
			delete(publishedBuckets, eventID)
		}
		publishMu.Unlock()
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		releaseReservation()
		return false, fmt.Errorf("marshal analytics rollup job: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(string(body)),
	})
	if err != nil {
		releaseReservation()
		return false, fmt.Errorf("publish analytics rollup job: %w", err)
	}
	return true, nil
}

func reserveAnalyticsPublish(eventID uuid.UUID, bucket time.Time) bool {
	publishMu.Lock()
	defer publishMu.Unlock()

	// Reservations are only useful inside the active minute. Prune older
	// entries so traffic across many historical events cannot grow this map for
	// the lifetime of the API process.
	for publishedEventID, publishedBucket := range publishedBuckets {
		if publishedBucket.Before(bucket) {
			delete(publishedBuckets, publishedEventID)
		}
	}
	if publishedBuckets[eventID].Equal(bucket) {
		return false
	}

	// Reserve before the network call so a burst of analytics reads emits one
	// message. The caller releases this reservation if publishing fails.
	publishedBuckets[eventID] = bucket
	return true
}

func buildAnalyticsRollupEnvelope(eventID uuid.UUID, now time.Time) analyticsRollupEnvelope {
	now = now.UTC()
	bucket := now.Truncate(analyticsJobBucket)
	jobID := uuid.NewV5(
		uuid.NamespaceURL,
		fmt.Sprintf("eventiapp:%s:%s:%s", analyticsJobType, eventID, bucket.Format(time.RFC3339)),
	)
	return analyticsRollupEnvelope{
		SchemaVersion: jobSchemaVersion,
		JobID:         jobID,
		OccurredAt:    now,
		Type:          analyticsJobType,
		Payload: analyticsRollupPayload{
			EventID:     eventID,
			RequestedAt: now,
			Trigger:     "analytics_read",
		},
	}
}
