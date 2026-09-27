// Package automationqueuerepository owns the isolated ITBEM -> local agent SQS hand-off.
package automationqueuerepository

import (
	"context"
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"strconv"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const publishTimeout = 5 * time.Second
const healthTimeout = 2 * time.Second
const leaseTimeout = 25 * time.Second

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
		TaskID string `json:"task_id"`
		// ProjectID is an optional scheduler fairness hint. It does not grant
		// access and is revalidated by the control plane when the task runs.
		ProjectID string `json:"project_id,omitempty"`
		AgentKey  string `json:"agent_key,omitempty"`
		// PlanStepID is an optional step-scoped child-task intent. Persisted
		// assignment authorization remains the control plane's responsibility.
		PlanStepID string `json:"plan_step_id,omitempty"`
		// TargetMachineID is hydrated from the persisted plan-step assignment at
		// publish time. An outbox/client value is never authoritative.
		TargetMachineID     string `json:"target_machine_id,omitempty"`
		Operation           string `json:"operation"`
		MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
		InputRef            string `json:"input_ref"`
		Attempt             int    `json:"attempt"`
		// RetryOfTaskID is set only for an explicitly authorized code-review
		// retry. Consumers keep ordinary deliveries strictly idempotent.
		RetryOfTaskID string `json:"retry_of_task_id,omitempty"`
	} `json:"payload"`
}

// LeasedMessage is the smallest control-plane projection required by the
// HTTPS execution gateway. ReceiptHandle must never be logged or returned to
// an execution host directly; the controller seals it into an authenticated
// lease token before crossing the trust boundary.
type LeasedMessage struct {
	Body          string
	ReceiptHandle string
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

var planStepAgentKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

func Init(region, accessKeyID, secretAccessKey, legacyURL, deadLetterURL, lanesJSON, roleDeadLetterURL, endpoint string) error {
	once.Do(func() {
		targets, initErr = parseQueueTargets(legacyURL, deadLetterURL, lanesJSON, roleDeadLetterURL)
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
	for _, configured := range []string{result.legacyURL, result.deadLetterURL, roleDeadLetterURL} {
		if configured != "" && !validQueueURL(configured) {
			return queueTargets{}, fmt.Errorf("automation queue target must be HTTPS or a loopback development URL")
		}
	}
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
		if queueURL == "" || !validQueueURL(queueURL) {
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

func validQueueURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1" || strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".localhost.localstack.cloud"))
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

func (target queueTargets) queueURLForLane(lane agentwork.Lane) (string, error) {
	if len(target.laneURLs) != len(orderedLanes()) {
		return "", fmt.Errorf("role-lane automation queues are unavailable")
	}
	if queueURL := strings.TrimSpace(target.laneURLs[lane]); queueURL != "" {
		return queueURL, nil
	}
	return "", fmt.Errorf("automation queue lane %s is unavailable", lane)
}

// ReceiveLane leases work on behalf of an outbound-only execution host. AWS
// credentials remain in the API process; callers receive an opaque gateway
// lease instead of this raw receipt handle.
func ReceiveLane(ctx context.Context, lane agentwork.Lane, limit int) ([]LeasedMessage, error) {
	if client == nil || limit < 1 || limit > 10 {
		return nil, fmt.Errorf("automation queue lease request is invalid")
	}
	queueURL, err := targets.queueURLForLane(lane)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, leaseTimeout)
	defer cancel()
	// This call is made through the HTTPS gateway, not by a process with a
	// direct SQS connection.  Do not hold an inbound HTTP request open for the
	// queue's 20-second long-poll interval: an intermediary timeout would turn
	// an otherwise healthy empty queue into a retryable 5xx and strand review
	// work.  The local worker owns paced polling/backoff, so a short receive
	// here preserves queue semantics without coupling them to proxy timeouts.
	response, err := client.ReceiveMessage(requestCtx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: int32(limit), WaitTimeSeconds: 0, VisibilityTimeout: 900})
	if err != nil {
		return nil, fmt.Errorf("lease automation message: %w", err)
	}
	result := make([]LeasedMessage, 0, len(response.Messages))
	for _, raw := range response.Messages {
		if raw.Body == nil || raw.ReceiptHandle == nil {
			continue
		}
		var message Message
		if err := json.Unmarshal([]byte(*raw.Body), &message); err != nil || Validate(message) != nil {
			continue
		}
		assignment, ok := agentwork.AssignmentForOperation(message.Payload.Operation)
		if !ok || assignment.Lane != lane {
			continue
		}
		result = append(result, LeasedMessage{Body: *raw.Body, ReceiptHandle: *raw.ReceiptHandle})
	}
	return result, nil
}

func ChangeLaneVisibility(ctx context.Context, lane agentwork.Lane, receiptHandle string, seconds int32) error {
	if client == nil || strings.TrimSpace(receiptHandle) == "" || seconds < 1 || seconds > 43_200 {
		return fmt.Errorf("automation queue visibility request is invalid")
	}
	queueURL, err := targets.queueURLForLane(lane)
	if err != nil {
		return err
	}
	_, err = client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle), VisibilityTimeout: seconds})
	if err != nil {
		return fmt.Errorf("change automation message visibility: %w", err)
	}
	return nil
}

func DeleteLaneMessage(ctx context.Context, lane agentwork.Lane, receiptHandle string) error {
	if client == nil || strings.TrimSpace(receiptHandle) == "" {
		return fmt.Errorf("automation queue acknowledgement is invalid")
	}
	queueURL, err := targets.queueURLForLane(lane)
	if err != nil {
		return err
	}
	_, err = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle)})
	if err != nil {
		return fmt.Errorf("acknowledge automation message: %w", err)
	}
	return nil
}

// ProbeLane performs the non-consuming SQS request used by an execution
// gateway preflight. Configuration presence alone is insufficient: the API
// process must be able to reach the exact lane with its runtime IAM identity.
func ProbeLane(ctx context.Context, lane agentwork.Lane) error {
	if client == nil {
		return fmt.Errorf("automation queue client is unavailable")
	}
	queueURL, err := targets.queueURLForLane(lane)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	if _, _, _, ok := queueCounts(requestCtx, queueURL); !ok {
		return fmt.Errorf("automation queue lane %s is unreachable", lane)
	}
	return nil
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
	if strings.TrimSpace(message.Payload.PlanStepID) != "" {
		var err error
		message, err = hydratePersistedPlanStepTarget(configuration.DB, message)
		if err != nil {
			return err
		}
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

// hydratePersistedPlanStepTarget treats the database assignment as the only
// source of routing authority. Values present in the serialized outbox body
// are overwritten, so a stale or forged target cannot move work to another
// local machine/profile.
func hydratePersistedPlanStepTarget(db *gorm.DB, message Message) (Message, error) {
	if db == nil {
		return Message{}, fmt.Errorf("plan-step machine target is unavailable")
	}
	taskID, taskErr := uuid.FromString(strings.TrimSpace(message.Payload.TaskID))
	stepID, stepErr := uuid.FromString(strings.TrimSpace(message.Payload.PlanStepID))
	if taskErr != nil || taskID == uuid.Nil || stepErr != nil || stepID == uuid.Nil {
		return Message{}, fmt.Errorf("plan-step machine target is invalid")
	}
	var assignment struct {
		TargetMachineID string `gorm:"column:target_machine_id"`
		TargetAgentKey  string `gorm:"column:target_agent_key"`
	}
	if err := db.Table("delivery_plan_step_assignments").
		Select("target_machine_id, target_agent_key").
		Where("child_automation_task_id = ? AND delivery_plan_step_id = ? AND status IN ?", taskID, stepID, []string{
			"pending", "queued", "dispatched", "running",
		}).Take(&assignment).Error; err != nil {
		return Message{}, fmt.Errorf("persisted plan-step machine target is unavailable: %w", err)
	}
	if strings.TrimSpace(assignment.TargetMachineID) == "" || strings.TrimSpace(assignment.TargetAgentKey) == "" ||
		!uuidValid(assignment.TargetMachineID) || assignment.TargetAgentKey != strings.TrimSpace(assignment.TargetAgentKey) || !planStepAgentKeyPattern.MatchString(assignment.TargetAgentKey) {
		return Message{}, fmt.Errorf("persisted plan-step machine target is incomplete")
	}
	message.Payload.AgentKey = assignment.TargetAgentKey
	message.Payload.TargetMachineID = assignment.TargetMachineID
	return message, nil
}

func uuidValid(value string) bool {
	parsed, err := uuid.FromString(strings.TrimSpace(value))
	return err == nil && parsed != uuid.Nil
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
	if agentKey := message.Payload.AgentKey; agentKey != "" {
		if agentKey != strings.TrimSpace(agentKey) || !planStepAgentKeyPattern.MatchString(agentKey) {
			return fmt.Errorf("automation agent profile is invalid")
		}
	}
	if planStepID := message.Payload.PlanStepID; planStepID != "" {
		if planStepID != strings.TrimSpace(planStepID) || !validUUID(planStepID) || message.Payload.Operation != "delivery.implementation" ||
			!uuidValid(message.Payload.TargetMachineID) || strings.TrimSpace(message.Payload.AgentKey) == "" {
			return fmt.Errorf("automation plan-step target is invalid")
		}
	} else if strings.TrimSpace(message.Payload.TargetMachineID) != "" {
		return fmt.Errorf("automation machine target requires a plan-step assignment")
	}
	return nil
}

func validUUID(value string) bool {
	parsed, err := uuid.FromString(value)
	return err == nil && parsed != uuid.Nil
}

// Keep the transport admission contract aligned with the isolated worker.
// The worker revalidates its decoded body as defense in depth; rejecting an
// invalid operation here prevents a malformed durable outbox payload from
// wasting receives and eventually occupying the shared automation DLQ.
func allowedOperation(operation string) bool {
	switch operation {
	case agentwork.OperationAIChat, agentwork.OperationDocumentAnalyze, agentwork.OperationCodeReview,
		agentwork.OperationProductIdeate, agentwork.OperationDeliveryPlan, agentwork.OperationDeliveryImplementation,
		agentwork.OperationDeliveryAssessment, agentwork.OperationDeliveryOnboardingProbe, agentwork.OperationDeliveryPublish,
		agentwork.OperationDeliveryReleaseGate, agentwork.OperationDeliveryQA, agentwork.OperationDeliverySummary:
		return true
	default:
		return false
	}
}

func publishBody(routedQueue, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	_, err := client.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(routedQueue), MessageBody: aws.String(body)})
	if err != nil {
		return fmt.Errorf("publish automation message: %w", err)
	}
	return nil
}
