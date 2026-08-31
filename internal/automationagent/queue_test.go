package automationagent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeQueue struct{ deleted int }

func (q *fakeQueue) Receive(context.Context, int) ([]QueueMessage, error) { return nil, nil }
func (q *fakeQueue) Delete(context.Context, QueueMessage) error           { q.deleted++; return nil }

type heartbeatQueue struct {
	fakeQueue
	extensions atomic.Int32
}

type deferQueue struct {
	heartbeatQueue
	deferrals atomic.Int32
}

func (q *deferQueue) Defer(context.Context, QueueMessage, int32) error {
	q.deferrals.Add(1)
	return nil
}

func (q *heartbeatQueue) ExtendVisibility(context.Context, QueueMessage, int32) error {
	q.extensions.Add(1)
	return nil
}

func TestCompletionTokensForOperationKeepsPlansBoundedAndExpandsImplementation(t *testing.T) {
	if got := CompletionTokensForOperation("ai.chat"); got != DefaultCompletionTokens {
		t.Fatalf("generic operation budget = %d, want %d", got, DefaultCompletionTokens)
	}
	if got := CompletionTokensForOperation("delivery.plan"); got != deliveryPlanCompletionLimit {
		t.Fatalf("delivery plan budget = %d, want %d", got, deliveryPlanCompletionLimit)
	}
	if got := CompletionTokensForOperation("code.review"); got != codeReviewCompletionLimit {
		t.Fatalf("code review budget = %d, want %d", got, codeReviewCompletionLimit)
	}
	if got := CompletionTokensForOperation("delivery.implementation"); got != deliveryImplementationCompletionLimit {
		t.Fatalf("delivery implementation budget = %d, want %d", got, deliveryImplementationCompletionLimit)
	}
	if got := CompletionTokensForOperation("delivery.qa"); got != DefaultCompletionTokens {
		t.Fatalf("QA operation budget = %d, want %d", got, DefaultCompletionTokens)
	}
	if got := CompletionTokensForOperation("delivery.publish"); got != 0 {
		t.Fatalf("deterministic publication should have no model budget, got %d", got)
	}
	if got := CompletionTokensForOperation("delivery.release_gate"); got != 0 {
		t.Fatalf("release Gatekeeper completion tokens = %d, want 0", got)
	}
	if got := CompletionTokensForOperation("delivery.onboarding_probe"); got != 0 {
		t.Fatalf("onboarding probe completion tokens = %d, want 0", got)
	}
}

func TestBoundedCompletionTokensNeverLetsQueuePayloadRaiseOperationLimit(t *testing.T) {
	if got := messageCompletionTokens("delivery.plan", miniMaxM3CompletionLimit+1); got != deliveryPlanCompletionLimit {
		t.Fatalf("queue payload raised delivery limit to %d", got)
	}
	if got := messageCompletionTokens("delivery.plan", 1024); got != 1024 {
		t.Fatalf("queue payload did not retain stricter limit: %d", got)
	}
	if got := messageCompletionTokens("code.review", miniMaxM3CompletionLimit+1); got != codeReviewCompletionLimit {
		t.Fatalf("queue payload raised code review limit to %d", got)
	}
	if got := messageCompletionTokens("code.review", 8192); got != 8192 {
		t.Fatalf("queue payload did not retain stricter code review limit: %d", got)
	}
	if got := messageCompletionTokens("ai.chat", 0); got != DefaultCompletionTokens {
		t.Fatalf("zero queue payload should retain default: %d", got)
	}
}

func TestQueuePrioritizesStructuredCodeReviewsWithoutTrustingPayloadPriority(t *testing.T) {
	review := validMessage()
	review.Payload.Operation = "code.review"
	reviewRaw, _ := json.Marshal(review)
	ordinary := validMessage()
	ordinary.Payload.Operation = "ai.chat"
	ordinaryRaw, _ := json.Marshal(ordinary)
	if got := queueMessagePriority(QueueMessage{Body: string(reviewRaw)}); got != queuePriorityReview {
		t.Fatalf("code review priority = %d, want review", got)
	}
	if got := queueMessagePriority(QueueMessage{Body: string(ordinaryRaw)}); got != queuePriorityNormal {
		t.Fatalf("ordinary task priority = %d, want normal", got)
	}
	if got := queueMessagePriority(QueueMessage{Body: `{"schema_version":1,"job_id":"job","tenant_code":"itbem","type":"ai.local.process","payload":{"task_id":"task","operation":"code.review","input_ref":"s3://itbem-ai-inputs-local/automation/inputs/task/input.json","attempt":1,"priority":"urgent"}}`}); got != queuePriorityNormal {
		t.Fatalf("unknown payload fields must not gain review priority, got %d", got)
	}
	if got := queueMessagePriority(QueueMessage{Body: `{not json`}); got != queuePriorityNormal {
		t.Fatalf("invalid queue payload must remain normal, got %d", got)
	}
}

func TestDecodeTaskMessageRejectsAmbiguousOrIncompleteQueuePayloads(t *testing.T) {
	message := validMessage()
	encoded, _ := json.Marshal(message)
	if _, err := DecodeTaskMessage(string(encoded) + ` {}`); err == nil {
		t.Fatal("multiple JSON values must be rejected")
	}
	if _, err := DecodeTaskMessage(`{"schema_version":1,"job_id":"job","tenant_code":"itbem","type":"ai.local.process","payload":{"task_id":"task","operation":"code.review","input_ref":"s3://itbem-ai-inputs-local/automation/inputs/task/input.json","attempt":1,"priority":"urgent"}}`); err == nil {
		t.Fatal("unknown fields must be rejected at the queue boundary")
	}
	message.Payload.Attempt = 0
	encoded, _ = json.Marshal(message)
	if _, err := DecodeTaskMessage(string(encoded)); err == nil {
		t.Fatal("queue messages must identify a positive delivery attempt")
	}
}

func TestReviewQueueBurstPolicyPreservesPriorityWithoutStarvingWork(t *testing.T) {
	makeRaw := func(operation, taskID string) QueueMessage {
		message := validMessage()
		message.Payload.Operation, message.Payload.TaskID = operation, taskID
		encoded, _ := json.Marshal(message)
		return QueueMessage{Body: string(encoded)}
	}
	pendingReviews := []QueueMessage{makeRaw("code.review", "r1"), makeRaw("code.review", "r2"), makeRaw("code.review", "r3"), makeRaw("code.review", "r4")}
	pendingWork := []QueueMessage{makeRaw("delivery.qa", "qa")}
	reviewSlotOccupied := false
	reviewsSinceWork := 0
	next := func() QueueMessage {
		if len(pendingWork) > 0 && (len(pendingReviews) == 0 || reviewSlotOccupied || reviewsSinceWork >= queueReviewBurstLimit) {
			message := pendingWork[0]
			pendingWork = pendingWork[1:]
			reviewsSinceWork = 0
			return message
		}
		if len(pendingReviews) > 0 && !reviewSlotOccupied {
			message := pendingReviews[0]
			pendingReviews = pendingReviews[1:]
			reviewsSinceWork++
			return message
		}
		message := pendingWork[0]
		pendingWork = pendingWork[1:]
		reviewsSinceWork = 0
		return message
	}
	for index := 0; index < queueReviewBurstLimit; index++ {
		if queueMessagePriority(next()) != queuePriorityReview {
			t.Fatalf("review %d should retain priority", index+1)
		}
	}
	if queueMessagePriority(next()) != queuePriorityNormal {
		t.Fatal("ordinary work must receive a turn after the bounded review burst")
	}
}

func TestProcessQueueMessageDeletesOnlyTerminalWork(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "hello"})
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	encoded, _ := json.Marshal(message)
	queue := &fakeQueue{}
	if err := ProcessQueueMessage(context.Background(), worker, queue, QueueMessage{Body: string(encoded), ReceiptHandle: "receipt"}); err != nil {
		t.Fatal(err)
	}
	if queue.deleted != 1 {
		t.Fatalf("expected terminal work deletion, got %d", queue.deleted)
	}
}

func TestProcessQueueMessageRetainsRetryableWork(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "hello"})
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, fakeProvider{err: &RetryableError{Message: "retry"}})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	encoded, _ := json.Marshal(message)
	queue := &fakeQueue{}
	if err := ProcessQueueMessage(context.Background(), worker, queue, QueueMessage{Body: string(encoded), ReceiptHandle: "receipt"}); err == nil {
		t.Fatal("expected retryable error")
	}
	if queue.deleted != 0 {
		t.Fatal("retryable work must remain in SQS")
	}
}

func TestRetryVisibilitySecondsIsBoundedAndHonorsProviderDelay(t *testing.T) {
	if got := retryVisibilitySeconds(nil); got != 120 {
		t.Fatalf("default retry visibility = %d, want 120", got)
	}
	if got := retryVisibilitySeconds(&RetryableError{RetryAfter: time.Second}); got != 30 {
		t.Fatalf("minimum retry visibility = %d, want 30", got)
	}
	if got := retryVisibilitySeconds(&RetryableError{RetryAfter: 91*time.Second + 100*time.Millisecond}); got != 92 {
		t.Fatalf("retry visibility must round up, got %d", got)
	}
	if got := retryVisibilitySeconds(&RetryableError{RetryAfter: time.Hour}); got != 900 {
		t.Fatalf("maximum retry visibility = %d, want 900", got)
	}
}

func TestQueueDeferredReviewDelayStaysWithinSQSBounds(t *testing.T) {
	if queueDeferredReviewSeconds < 1 || queueDeferredReviewSeconds > 43_200 {
		t.Fatalf("review defer delay must be valid for SQS, got %d", queueDeferredReviewSeconds)
	}
	queue := &deferQueue{}
	if err := queue.Defer(context.Background(), QueueMessage{ReceiptHandle: "receipt"}, queueDeferredReviewSeconds); err != nil {
		t.Fatal(err)
	}
	if queue.deferrals.Load() != 1 {
		t.Fatal("queued review should be explicitly deferred when the review lane is occupied")
	}
}

func TestLongRunningQueueMessageRenewsItsVisibilityLease(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "hello"})
	provider := &concurrentProvider{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	encoded, _ := json.Marshal(message)
	queue := &heartbeatQueue{}
	if err := processQueueMessageWithVisibilityHeartbeat(context.Background(), worker, queue, QueueMessage{Body: string(encoded), ReceiptHandle: "receipt"}, nil, 5*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if queue.extensions.Load() < 1 {
		t.Fatal("long-running provider work must renew its SQS visibility lease")
	}
	if queue.deleted != 1 {
		t.Fatal("completed work must still delete the SQS message after heartbeat shutdown")
	}
}

type drainingQueue struct {
	mu       sync.Mutex
	messages []QueueMessage
	deleted  int
	cancel   context.CancelFunc
}

func (q *drainingQueue) Receive(ctx context.Context, limit int) ([]QueueMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.messages) == 0 {
		return nil, nil
	}
	if limit > len(q.messages) {
		limit = len(q.messages)
	}
	result := append([]QueueMessage(nil), q.messages[:limit]...)
	q.messages = q.messages[limit:]
	return result, nil
}

func (q *drainingQueue) Delete(_ context.Context, _ QueueMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deleted++
	if q.deleted == 3 {
		q.cancel()
	}
	return nil
}

type concurrentProvider struct {
	active  atomic.Int32
	maximum atomic.Int32
}

type reviewTrackingProvider struct {
	concurrentProvider
	reviewActive  atomic.Int32
	reviewMaximum atomic.Int32
}

func (p *reviewTrackingProvider) Complete(_ context.Context, messages []Message, _ int) (Completion, error) {
	isReview := false
	for _, message := range messages {
		if strings.Contains(message.Content, "rigorous pull-request reviewer") {
			isReview = true
			break
		}
	}
	if isReview {
		active := p.reviewActive.Add(1)
		for {
			maximum := p.reviewMaximum.Load()
			if active <= maximum || p.reviewMaximum.CompareAndSwap(maximum, active) {
				break
			}
		}
		defer p.reviewActive.Add(-1)
	}
	time.Sleep(25 * time.Millisecond)
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: "{}", Usage: map[string]any{}}, nil
}

func (p *concurrentProvider) Complete(context.Context, []Message, int) (Completion, error) {
	active := p.active.Add(1)
	for {
		maximum := p.maximum.Load()
		if active <= maximum || p.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	p.active.Add(-1)
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: "ok", Usage: map[string]any{}}, nil
}

func TestRunQueueHonorsConfiguredConcurrency(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "hello"})
	provider := &concurrentProvider{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make([]QueueMessage, 0, 3)
	for index := 0; index < 3; index++ {
		message := validMessage()
		message.Payload.Operation = "ai.chat"
		message.Payload.TaskID = string(rune('a' + index))
		encoded, _ := json.Marshal(message)
		messages = append(messages, QueueMessage{Body: string(encoded), ReceiptHandle: string(rune('1' + index))})
	}
	queue := &drainingQueue{messages: messages, cancel: cancel}
	if err := RunQueue(ctx, worker, queue, 2, nil); err != nil {
		t.Fatal(err)
	}
	if provider.maximum.Load() > 2 || provider.maximum.Load() < 2 {
		t.Fatalf("unexpected max provider concurrency: %d", provider.maximum.Load())
	}
}

func TestRunQueueSerializesCodeReviewJobsWhileOtherWorkCanUseCapacity(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "review", Delivery: validCodeReviewInput()})
	provider := &reviewTrackingProvider{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	messages := make([]QueueMessage, 0, 3)
	for index, operation := range []string{"code.review", "code.review", "ai.chat"} {
		message := validMessage()
		message.Payload.Operation = operation
		message.Payload.TaskID = string(rune('r' + index))
		encoded, _ := json.Marshal(message)
		messages = append(messages, QueueMessage{Body: string(encoded), ReceiptHandle: string(rune('4' + index))})
	}
	queue := &drainingQueue{messages: messages, cancel: cancel}
	if err := RunQueue(ctx, worker, queue, 3, nil); err != nil {
		t.Fatal(err)
	}
	if provider.reviewMaximum.Load() != 1 {
		t.Fatalf("code review jobs must serialize, got %d concurrent reviews", provider.reviewMaximum.Load())
	}
}
