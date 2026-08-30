// Package automationqueuerepository owns the isolated ITBEM -> local agent SQS hand-off.
package automationqueuerepository

import (
	"context"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const publishTimeout = 5 * time.Second
const healthTimeout = 2 * time.Second

// Health is a deliberately small queue projection. Approximate SQS counters
// are useful for service operation but must never be represented as exact
// task state; the database remains authoritative for individual tasks.
type Health struct {
	Available           bool                  `json:"available"`
	Visible             int64                 `json:"visible"`
	InFlight            int64                 `json:"in_flight"`
	Delayed             int64                 `json:"delayed"`
	DeadLetterAvailable bool                  `json:"dead_letter_available"`
	DeadLetterVisible   int64                 `json:"dead_letter_visible"`
	Lanes               map[string]LaneHealth `json:"lanes,omitempty"`
}

type LaneHealth struct {
	Available bool  `json:"available"`
	Visible   int64 `json:"visible"`
	InFlight  int64 `json:"in_flight"`
	Delayed   int64 `json:"delayed"`
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
	client  *sqs.Client
	targets queueTargets
	once    sync.Once
	initErr error
)

type queueTargets struct {
	legacyURL     string
	deadLetterURL string
	laneURLs      map[agentwork.Lane]string
}

func Init(region, accessKeyID, secretAccessKey, url, deadLetterURL, lanesJSON, roleDeadLetterURL, endpoint string) error {
	once.Do(func() {
		targets, initErr = parseQueueTargets(url, deadLetterURL, lanesJSON, roleDeadLetterURL)
		if initErr != nil || !targets.configured() {
			return
		}
		cfg, err := configuration.LoadAWSConfig(context.Background(), region, accessKeyID, secretAccessKey)
		if err != nil {
			initErr = fmt.Errorf("load automation queue AWS configuration: %w", err)
			return
		}
		client = sqs.NewFromConfig(cfg, configuration.SQSClientOptions(endpoint))
	})
	return initErr
}

func IsConfigured() bool { return client != nil && targets.configured() }

func parseQueueTargets(legacyURL, legacyDeadLetterURL, lanesJSON, roleDeadLetterURL string) (queueTargets, error) {
	result := queueTargets{legacyURL: strings.TrimSpace(legacyURL), deadLetterURL: strings.TrimSpace(legacyDeadLetterURL)}
	raw := strings.TrimSpace(lanesJSON)
	roleDeadLetterURL = strings.TrimSpace(roleDeadLetterURL)
	if raw == "" {
		if roleDeadLetterURL != "" {
			return queueTargets{}, fmt.Errorf("role dead-letter queue requires the complete role-lane map")
		}
		if result.legacyURL == "" && result.deadLetterURL != "" {
			return queueTargets{}, fmt.Errorf("legacy dead-letter queue requires the legacy automation queue")
		}
		return result, nil
	}

	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire struct {
		Orchestration string `json:"orchestration"`
		Engineering   string `json:"engineering"`
		Review        string `json:"review"`
		QA            string `json:"qa"`
		Release       string `json:"release"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return queueTargets{}, fmt.Errorf("decode role-lane queue map: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return queueTargets{}, fmt.Errorf("role-lane queue map must contain one JSON object")
	}
	result.laneURLs = map[agentwork.Lane]string{
		agentwork.LaneOrchestration: strings.TrimSpace(wire.Orchestration),
		agentwork.LaneEngineering:   strings.TrimSpace(wire.Engineering),
		agentwork.LaneReview:        strings.TrimSpace(wire.Review),
		agentwork.LaneQA:            strings.TrimSpace(wire.QA),
		agentwork.LaneRelease:       strings.TrimSpace(wire.Release),
	}
	if roleDeadLetterURL == "" {
		return queueTargets{}, fmt.Errorf("role-lane queue map requires its dead-letter queue")
	}
	seen := map[string]string{}
	if result.legacyURL != "" {
		seen[result.legacyURL] = "legacy"
	}
	for _, lane := range orderedLanes() {
		queueURL := result.laneURLs[lane]
		if queueURL == "" {
			return queueTargets{}, fmt.Errorf("role-lane queue map is missing %s", lane)
		}
		if previous, duplicate := seen[queueURL]; duplicate {
			return queueTargets{}, fmt.Errorf("role-lane queue %s duplicates %s", lane, previous)
		}
		seen[queueURL] = string(lane)
	}
	if previous, duplicate := seen[roleDeadLetterURL]; duplicate {
		return queueTargets{}, fmt.Errorf("role dead-letter queue duplicates %s", previous)
	}
	result.deadLetterURL = roleDeadLetterURL
	return result, nil
}

func orderedLanes() []agentwork.Lane {
	return []agentwork.Lane{agentwork.LaneOrchestration, agentwork.LaneEngineering, agentwork.LaneReview, agentwork.LaneQA, agentwork.LaneRelease}
}

func (target queueTargets) configured() bool {
	return target.legacyURL != "" || len(target.laneURLs) == len(orderedLanes())
}

func (target queueTargets) queueURLForOperation(operation string) (string, error) {
	assignment, ok := agentwork.AssignmentForOperation(operation)
	if !ok {
		return "", fmt.Errorf("automation operation is not allowlisted")
	}
	if len(target.laneURLs) > 0 {
		if queueURL := target.laneURLs[assignment.Lane]; queueURL != "" {
			return queueURL, nil
		}
		return "", fmt.Errorf("automation queue lane %s is unavailable", assignment.Lane)
	}
	if target.legacyURL == "" {
		return "", fmt.Errorf("ITBEM automation queue is unavailable")
	}
	return target.legacyURL, nil
}

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
	if len(targets.laneURLs) > 0 {
		health.Available = true
		health.Lanes = make(map[string]LaneHealth, len(targets.laneURLs))
		for _, lane := range orderedLanes() {
			visible, inFlight, delayed, ok := queueCounts(requestCtx, targets.laneURLs[lane])
			health.Lanes[string(lane)] = LaneHealth{Available: ok, Visible: visible, InFlight: inFlight, Delayed: delayed}
			health.Available = health.Available && ok
			health.Visible += visible
			health.InFlight += inFlight
			health.Delayed += delayed
		}
	} else if visible, inFlight, delayed, ok := queueCounts(requestCtx, targets.legacyURL); ok {
		health.Available, health.Visible, health.InFlight, health.Delayed = true, visible, inFlight, delayed
	}
	// A DLQ is a failure signal, not an input queue. Its depth is intentionally
	// exposed only as aggregate telemetry and never triggers automatic replay.
	if targets.deadLetterURL != "" {
		if visible, _, _, ok := queueCounts(requestCtx, targets.deadLetterURL); ok {
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
	queueURL, err := targets.queueURLForOperation(message.Payload.Operation)
	if err != nil {
		return err
	}
	return publishBody(queueURL, string(body))
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
	if !agentwork.IsSupportedOperation(message.Payload.Operation) {
		return fmt.Errorf("automation operation is not allowlisted")
	}
	return nil
}

func publishBody(queueURL, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(body)})
	if err != nil {
		return fmt.Errorf("publish automation message: %w", err)
	}
	return nil
}
