// Package automationqueuerepository owns the isolated ITBEM -> local agent SQS hand-off.
package automationqueuerepository

import (
	"context"
	"encoding/json"
	"events-stocks/configuration"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"strconv"
)

const publishTimeout = 5 * time.Second
const healthTimeout = 2 * time.Second

// Health is a deliberately small queue projection. Approximate SQS counters
// are useful for service operation but must never be represented as exact
// task state; the database remains authoritative for individual tasks.
type Health struct {
	Available           bool  `json:"available"`
	Visible             int64 `json:"visible"`
	InFlight            int64 `json:"in_flight"`
	Delayed             int64 `json:"delayed"`
	DeadLetterAvailable bool  `json:"dead_letter_available"`
	DeadLetterVisible   int64 `json:"dead_letter_visible"`
}

type Message struct {
	SchemaVersion int    `json:"schema_version"`
	JobID         string `json:"job_id"`
	TenantCode    string `json:"tenant_code"`
	CorrelationID string `json:"correlation_id"`
	Type          string `json:"type"`
	Payload       struct {
		TaskID              string `json:"task_id"`
		Operation           string `json:"operation"`
		MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
		InputRef            string `json:"input_ref"`
		Attempt             int    `json:"attempt"`
	} `json:"payload"`
}

var (
	client             *sqs.Client
	queueURL           string
	deadLetterQueueURL string
	once               sync.Once
)

func Init(region, accessKeyID, secretAccessKey, url, deadLetterURL, endpoint string) {
	once.Do(func() {
		queueURL = strings.TrimSpace(url)
		deadLetterQueueURL = strings.TrimSpace(deadLetterURL)
		if queueURL == "" {
			return
		}
		cfg, err := configuration.LoadAWSConfig(context.Background(), region, accessKeyID, secretAccessKey)
		if err != nil {
			return
		}
		client = sqs.NewFromConfig(cfg, configuration.SQSClientOptions(endpoint))
	})
}

func IsConfigured() bool { return client != nil && queueURL != "" }

// QueueHealth obtains best-effort approximate queue depth with a short,
// bounded request. A failed read deliberately returns Available=false rather
// than inventing zero backlog, so an operations surface can distinguish an
// idle queue from unavailable telemetry.
func QueueHealth(ctx context.Context) Health {
	if !IsConfigured() {
		return Health{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	health := Health{}
	if visible, inFlight, delayed, ok := queueCounts(requestCtx, queueURL); ok {
		health.Available, health.Visible, health.InFlight, health.Delayed = true, visible, inFlight, delayed
	}
	// A DLQ is a failure signal, not an input queue. Its depth is intentionally
	// exposed only as aggregate telemetry and never triggers automatic replay.
	if deadLetterQueueURL != "" {
		if visible, _, _, ok := queueCounts(requestCtx, deadLetterQueueURL); ok {
			health.DeadLetterAvailable, health.DeadLetterVisible = true, visible
		}
	}
	return health
}

func queueCounts(ctx context.Context, url string) (visible, inFlight, delayed int64, ok bool) {
	response, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible, types.QueueAttributeNameApproximateNumberOfMessagesDelayed},
	})
	if err != nil {
		return 0, 0, 0, false
	}
	return queueCountsFromAttributes(response.Attributes)
}

func queueCountsFromAttributes(attributes map[string]string) (visible, inFlight, delayed int64, ok bool) {
	visible, okVisible := queueAttributeCount(attributes, types.QueueAttributeNameApproximateNumberOfMessages)
	inFlight, okInFlight := queueAttributeCount(attributes, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)
	delayed, okDelayed := queueAttributeCount(attributes, types.QueueAttributeNameApproximateNumberOfMessagesDelayed)
	return visible, inFlight, delayed, okVisible && okInFlight && okDelayed
}

func queueAttributeCount(attributes map[string]string, name types.QueueAttributeName) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(attributes[string(name)]), 10, 64)
	return value, err == nil && value >= 0
}

func Publish(message Message) error {
	if !IsConfigured() {
		return fmt.Errorf("ITBEM automation queue is unavailable")
	}
	if err := Validate(message); err != nil {
		return err
	}
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal automation message: %w", err)
	}
	return publishBody(string(body))
}

// PublishSerialized delivers a previously validated outbox payload. It
// decodes and validates again so the durable outbox cannot become a bypass
// around the tenant and message-type boundary.
func PublishSerialized(body string) error {
	var message Message
	if err := json.Unmarshal([]byte(body), &message); err != nil {
		return fmt.Errorf("decode automation message: %w", err)
	}
	return Publish(message)
}

func Validate(message Message) error {
	if message.SchemaVersion != 1 || message.TenantCode != "itbem" || message.Type != "ai.local.process" || strings.TrimSpace(message.JobID) == "" || strings.TrimSpace(message.Payload.TaskID) == "" || message.Payload.Attempt < 1 {
		return fmt.Errorf("invalid ITBEM automation message")
	}
	if !allowedOperation(message.Payload.Operation) {
		return fmt.Errorf("automation operation is not allowlisted")
	}
	return nil
}

// Keep the transport admission contract aligned with the isolated worker.
// The worker revalidates its decoded body as defense in depth; rejecting an
// invalid operation here prevents a malformed durable outbox payload from
// wasting receives and eventually occupying the shared automation DLQ.
func allowedOperation(operation string) bool {
	switch operation {
	case "ai.chat", "document.analyze", "code.review", "product.ideate", "delivery.plan", "delivery.implementation", "delivery.publish", "delivery.qa", "delivery.summary":
		return true
	default:
		return false
	}
}

func publishBody(body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(body)})
	if err != nil {
		return fmt.Errorf("publish automation message: %w", err)
	}
	return nil
}
