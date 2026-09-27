package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/internal/inferencecapability"
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
	lastDelay atomic.Int32
}

func (q *deferQueue) Defer(_ context.Context, _ QueueMessage, delay int32) error {
	q.deferrals.Add(1)
	q.lastDelay.Store(delay)
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
	if got := CompletionTokensForOperation("delivery.plan"); got != DefaultCompletionTokens {
		t.Fatalf("delivery plan budget = %d, want %d", got, DefaultCompletionTokens)
	}
	if got := CompletionTokensForOperation("delivery.chat"); got != DefaultCompletionTokens {
		t.Fatalf("delivery chat budget = %d, want %d", got, DefaultCompletionTokens)
	}
	if got := CompletionTokensForOperation("delivery.implementation"); got != miniMaxM3CompletionLimit {
		t.Fatalf("delivery implementation budget = %d, want %d", got, miniMaxM3CompletionLimit)
	}
	if got := CompletionTokensForOperation("product.ideate"); got != miniMaxM3CompletionLimit {
		t.Fatalf("product ideation budget = %d, want %d", got, miniMaxM3CompletionLimit)
	}
	if got := CompletionTokensForOperation("code.review"); got != miniMaxM3CompletionLimit {
		t.Fatalf("segmented code review budget = %d, want %d", got, miniMaxM3CompletionLimit)
	}
	if got := CompletionTokensForOperation("delivery.qa"); got != DefaultCompletionTokens {
		t.Fatalf("QA operation budget = %d, want %d", got, DefaultCompletionTokens)
	}
	if got := CompletionTokensForOperation("delivery.publish"); got != 0 {
		t.Fatalf("deterministic publication should have no model budget, got %d", got)
	}
}

func TestBoundedCompletionTokensNeverLetsQueuePayloadRaiseOperationLimit(t *testing.T) {
	if got := messageCompletionTokens("delivery.plan", miniMaxM3CompletionLimit+1); got != DefaultCompletionTokens {
		t.Fatalf("queue payload raised delivery limit to %d", got)
	}
	if got := messageCompletionTokens("delivery.plan", 1024); got != 1024 {
		t.Fatalf("queue payload did not retain stricter limit: %d", got)
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

func TestSpecialistQueueDefersUnsupportedCapabilityBeforeClaim(t *testing.T) {
	worker, err := NewWorker(WorkerConfig{
		InputBucket:       "itbem-ai-inputs-local",
		OutputBucket:      "itbem-ai-outputs-local",
		AllowedOperations: []string{"delivery.plan"},
	}, &fakeStore{}, &fakeCallback{}, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "delivery.qa"
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	queue := &deferQueue{}
	if !deferUnsupportedCapability(context.Background(), worker, queue, QueueMessage{Body: string(raw), ReceiptHandle: "specialist"}, nil) {
		t.Fatal("unsupported specialist work should be deferred when the transport supports it")
	}
	if queue.deferrals.Load() != 1 || queue.lastDelay.Load() != queueDeferredCapabilitySeconds {
		t.Fatalf("unexpected capability deferral: count=%d delay=%d", queue.deferrals.Load(), queue.lastDelay.Load())
	}
}

func TestTargetedChildDefersOnSpecialistMismatchBeforeClaim(t *testing.T) {
	targetMachineID := stepCallbackUUID()
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{
		InputBucket:       "itbem-ai-inputs-local",
		OutputBucket:      "itbem-ai-outputs-local",
		AllowedOperations: []string{"delivery.implementation"},
		AgentKey:          "backend-engineer",
		MachineID:         targetMachineID,
	}, &fakeStore{}, callback, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "delivery.implementation"
	message.Payload.PlanStepID = stepCallbackUUID()
	message.Payload.TargetMachineID = targetMachineID
	message.Payload.AgentKey = "frontend-engineer"
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	queueMessage := QueueMessage{Body: string(raw), ReceiptHandle: "wrong-specialist"}
	if worker.canProcessMessage(queueMessage) {
		t.Fatal("worker should not admit another specialist's targeted child task")
	}
	queue := &deferQueue{}
	if !deferUnsupportedCapability(context.Background(), worker, queue, queueMessage, nil) {
		t.Fatal("specialist mismatch should use the existing capability deferral")
	}
	if queue.deferrals.Load() != 1 || queue.lastDelay.Load() != queueDeferredCapabilitySeconds {
		t.Fatalf("unexpected specialist deferral: count=%d delay=%d", queue.deferrals.Load(), queue.lastDelay.Load())
	}

	message.Payload.AgentKey = worker.config.AgentKey
	matchingRaw, _ := json.Marshal(message)
	if !worker.canProcessMessage(QueueMessage{Body: string(matchingRaw)}) {
		t.Fatal("targeted task assigned to this worker profile should be admitted")
	}
	message.Payload.AgentKey = "frontend-engineer"

	var retryable *RetryableError
	if err := worker.Process(context.Background(), message); !errors.As(err, &retryable) {
		t.Fatalf("direct processing must retry/defer instead of terminalizing mismatch: %v", err)
	}
	if len(callback.updates) != 0 {
		t.Fatalf("profile mismatch reached task claim/status update: %#v", callback.updates)
	}

	message.Payload.AgentKey = ""
	legacyTarget, _ := json.Marshal(message)
	if _, err := DecodeTaskMessage(string(legacyTarget)); err == nil {
		t.Fatal("targeted assignments must carry the persisted profile target")
	}
}

func TestTargetedChildAdmitsProfileForCloudValidatedMachineFailover(t *testing.T) {
	targetMachineID := stepCallbackUUID()
	newWorker := func(workerID string) *Worker {
		worker, err := NewWorker(WorkerConfig{
			InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local",
			WorkerID: workerID, AgentKey: "generalist", MachineID: targetMachineID,
			AllowedOperations: []string{"delivery.implementation"},
		}, &fakeStore{}, &fakeCallback{}, fakeProvider{})
		if err != nil {
			t.Fatal(err)
		}
		return worker
	}
	message := validMessage()
	message.Payload.Operation = "delivery.implementation"
	message.Payload.PlanStepID = stepCallbackUUID()
	message.Payload.AgentKey = "generalist"
	message.Payload.TargetMachineID = targetMachineID
	firstProcess := newWorker(stepCallbackUUID())
	if !firstProcess.canProcessMessage(queueMessageForTest(t, message)) {
		t.Fatal("assigned machine should admit its original worker process")
	}
	replacement := newWorker(stepCallbackUUID())
	if !replacement.canProcessMessage(queueMessageForTest(t, message)) {
		t.Fatal("same stable machine should admit a replacement WorkerID after restart")
	}

	otherMachine := newWorker(stepCallbackUUID())
	otherMachine.config.MachineID = stepCallbackUUID()
	otherMachineRaw := queueMessageForTest(t, message)
	if !otherMachine.canProcessMessage(otherMachineRaw) {
		t.Fatal("a same-profile replacement must receive redelivery; the cloud validates whether its leases and workspace permit failover")
	}

	wrongProfile := newWorker(stepCallbackUUID())
	wrongProfile.config.AgentKey = "qa-specialist"
	wrongProfileRaw := queueMessageForTest(t, message)
	queue := &deferQueue{}
	if !deferUnsupportedCapability(context.Background(), wrongProfile, queue, wrongProfileRaw, nil) {
		t.Fatal("wrong profile should defer/retry rather than consume the assignment")
	}
	if queue.deferrals.Load() != 1 {
		t.Fatalf("wrong profile deferrals = %d, want 1", queue.deferrals.Load())
	}
	var retryable *RetryableError
	if err := wrongProfile.Process(context.Background(), message); !errors.As(err, &retryable) {
		t.Fatalf("direct wrong-profile processing should be retryable, got %v", err)
	}
}

func queueMessageForTest(t *testing.T, message TaskMessage) QueueMessage {
	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return QueueMessage{Body: string(raw), ReceiptHandle: "targeted"}
}

type capabilityDispatchQueue struct {
	deferQueue
	message QueueMessage
	cancel  context.CancelFunc
	sent    atomic.Bool
}

func (q *capabilityDispatchQueue) Receive(context.Context, int) ([]QueueMessage, error) {
	if q.sent.Swap(true) {
		return nil, nil
	}
	return []QueueMessage{q.message}, nil
}

func (q *capabilityDispatchQueue) Defer(ctx context.Context, message QueueMessage, delay int32) error {
	if err := q.deferQueue.Defer(ctx, message, delay); err != nil {
		return err
	}
	if q.cancel != nil {
		q.cancel()
	}
	return nil
}

func TestRunQueueRoutesUnsupportedWorkBeforeProviderAdmission(t *testing.T) {
	worker, err := NewWorker(WorkerConfig{
		InputBucket:       "itbem-ai-inputs-local",
		OutputBucket:      "itbem-ai-outputs-local",
		AllowedOperations: []string{"delivery.plan"},
	}, &fakeStore{}, &fakeCallback{}, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "delivery.qa"
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	queue := &capabilityDispatchQueue{message: QueueMessage{Body: string(raw), ReceiptHandle: "route-before-claim"}, cancel: cancel}
	if err := RunQueue(ctx, worker, queue, 1, nil); err != nil {
		t.Fatal(err)
	}
	if queue.deferrals.Load() != 1 || queue.lastDelay.Load() != queueDeferredCapabilitySeconds {
		t.Fatalf("unsupported delivery was not deferred before admission: count=%d delay=%d", queue.deferrals.Load(), queue.lastDelay.Load())
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

func TestDecodeTaskMessageSupportsOptionalTargetPlanStepID(t *testing.T) {
	legacy := validMessage()
	legacyEncoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTaskMessage(string(legacyEncoded)); err != nil {
		t.Fatalf("legacy message without target step must remain valid: %v", err)
	}

	targeted := validMessage()
	targeted.Payload.Operation = "delivery.implementation"
	targeted.Payload.PlanStepID = stepCallbackUUID()
	targeted.Payload.AgentKey = "generalist"
	targeted.Payload.TargetMachineID = stepCallbackUUID()
	targetedEncoded, err := json.Marshal(targeted)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTaskMessage(string(targetedEncoded))
	if err != nil || decoded.Payload.PlanStepID != targeted.Payload.PlanStepID {
		t.Fatalf("valid target plan-step id was not decoded: %#v, %v", decoded.Payload, err)
	}
	targeted.Payload.AgentKey = "backend-engineer"
	targetedEncoded, _ = json.Marshal(targeted)
	decoded, err = DecodeTaskMessage(string(targetedEncoded))
	if err != nil || decoded.Payload.AgentKey != targeted.Payload.AgentKey {
		t.Fatalf("valid targeted agent key was not decoded: %#v, %v", decoded.Payload, err)
	}

	for _, value := range []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		targeted.Payload.PlanStepID = value
		encoded, _ := json.Marshal(targeted)
		if _, err := DecodeTaskMessage(string(encoded)); err == nil {
			t.Fatalf("invalid target plan-step id %q was accepted", value)
		}
	}
	targeted.Payload.TargetMachineID = "not-a-machine"
	encoded, _ := json.Marshal(targeted)
	if _, err := DecodeTaskMessage(string(encoded)); err == nil {
		t.Fatal("targeted assignment without a valid machine identity was accepted")
	}
	targeted.Payload.TargetMachineID = stepCallbackUUID()
	targeted.Payload.PlanStepID = stepCallbackUUID()
	targeted.Payload.Operation = "delivery.plan"
	encoded, _ = json.Marshal(targeted)
	if _, err := DecodeTaskMessage(string(encoded)); err == nil {
		t.Fatal("target step id on a non-implementation task was accepted")
	}
	targeted.Payload.Operation = "delivery.implementation"
	for _, agentKey := range []string{"Backend Engineer", "a", " backend-engineer"} {
		targeted.Payload.AgentKey = agentKey
		encoded, _ = json.Marshal(targeted)
		if _, err := DecodeTaskMessage(string(encoded)); err == nil {
			t.Fatalf("malformed agent key %q was accepted", agentKey)
		}
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

func TestQueueOperationLaneRoundRobinsDistinctOperations(t *testing.T) {
	makeRaw := func(operation, taskID string) QueueMessage {
		message := validMessage()
		message.Payload.Operation, message.Payload.TaskID = operation, taskID
		encoded, _ := json.Marshal(message)
		return QueueMessage{Body: string(encoded)}
	}
	lane := queueOperationLane{queues: make(map[string][]QueueMessage)}
	lane.enqueue(makeRaw("delivery.qa", "qa-1"))
	lane.enqueue(makeRaw("delivery.qa", "qa-2"))
	lane.enqueue(makeRaw("delivery.chat", "chat-1"))
	lane.enqueue(makeRaw("delivery.plan", "plan-1"))

	got := make([]string, 0, 4)
	for index := 0; index < 4; index++ {
		message, ok := lane.next()
		if !ok {
			t.Fatalf("lane ended after %d messages", index)
		}
		decoded, err := DecodeTaskMessage(message.Body)
		if err != nil {
			t.Fatalf("decode scheduled message: %v", err)
		}
		got = append(got, queueOperation(message)+":"+decoded.Payload.TaskID)
	}
	want := []string{"delivery.qa:qa-1", "delivery.chat:chat-1", "delivery.plan:plan-1", "delivery.qa:qa-2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("operation lanes were not round-robin: got %v want %v", got, want)
	}
}

func TestQueueOperationLaneRoundRobinsProjectsBeforeAProjectCanMonopolize(t *testing.T) {
	makeRaw := func(project, operation, taskID string) QueueMessage {
		message := validMessage()
		message.Payload.ProjectID = project
		message.Payload.Operation = operation
		message.Payload.TaskID = taskID
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		return QueueMessage{Body: string(encoded)}
	}
	var lane queueOperationLane
	lane.queues = make(map[string][]QueueMessage)
	for _, message := range []QueueMessage{
		makeRaw("project-a", "delivery.qa", "a-1"),
		makeRaw("project-a", "delivery.qa", "a-2"),
		makeRaw("project-a", "delivery.qa", "a-3"),
		makeRaw("project-b", "delivery.qa", "b-1"),
		makeRaw("project-b", "delivery.qa", "b-2"),
	} {
		lane.enqueue(message)
	}
	got := make([]string, 0, 5)
	for len(got) < 5 {
		message, ok := lane.next()
		if !ok {
			t.Fatal("lane unexpectedly empty")
		}
		decoded, err := DecodeTaskMessage(message.Body)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, decoded.Payload.ProjectID+":"+decoded.Payload.TaskID)
	}
	want := []string{"project-a:a-1", "project-b:b-1", "project-a:a-2", "project-b:b-2", "project-a:a-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("project fairness order = %#v, want %#v", got, want)
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

type queueRecoveryProvider struct{ calls int }

func (p *queueRecoveryProvider) Complete(context.Context, []Message, int) (Completion, error) {
	p.calls++
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: "observed result", Usage: map[string]any{}}, nil
}

func TestQueueBusyRunRedeliverySurvivesUntilRecovery(t *testing.T) {
	claims, completions := 0, 0
	var worker *Worker
	var err error
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var update TaskUpdate
		if err := json.NewDecoder(request.Body).Decode(&update); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		switch update.Status {
		case "running":
			claims++
			if claims == 1 {
				// Another worker still holds the durable lease. Its eventual
				// crash must not erase this task's queue recovery path.
				writer.Header().Set("X-ITBEM-Automation-Run-Busy", "1")
				writer.WriteHeader(http.StatusConflict)
				return
			}
			identity := worker.identity()
			token, err := inferencecapability.Mint("test-only-server-signing-key-never-used-by-runtime", inferencecapability.Scope{
				TaskID: strings.TrimPrefix(request.URL.Path, "/api/internal/automation/tasks/"), RunID: update.RunID, Operation: "ai.chat",
				WorkerID: identity.WorkerID, AgentKey: identity.AgentKey, MachineID: identity.MachineID,
			}, time.Minute)
			if err != nil {
				t.Error(err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			writer.Header().Set(inferencecapability.HeaderName, token)
		case "completed":
			completions++
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback := newTestHTTPCallback(t, server)
	input, _ := json.Marshal(TaskInput{Prompt: "synthetic recovery"})
	store, provider := &fakeStore{input: input}, &queueRecoveryProvider{}
	worker, err = NewWorker(WorkerConfig{
		InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local",
		AgentKey: "generalist", MachineID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79827",
	}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	encoded, _ := json.Marshal(message)
	queue := &fakeQueue{}
	raw := QueueMessage{Body: string(encoded), ReceiptHandle: "duplicate-receipt"}
	err = ProcessQueueMessage(context.Background(), worker, queue, raw)
	var retryable *RetryableError
	if !errors.As(err, &retryable) || queue.deleted != 0 || provider.calls != 0 || len(store.writes) != 0 {
		t.Fatalf("busy redelivery must retain without inference or writes: error=%v deleted=%d calls=%d writes=%d", err, queue.deleted, provider.calls, len(store.writes))
	}
	// Simulate redelivery after the owner has failed and the backend permits
	// a new lease. There is no model network request or real queue in this test.
	raw.ReceiptHandle = "recovery-receipt"
	if err := ProcessQueueMessage(context.Background(), worker, queue, raw); err != nil {
		t.Fatal(err)
	}
	if queue.deleted != 1 || provider.calls != 1 || completions != 1 {
		t.Fatalf("recovery must complete and ACK once: deleted=%d calls=%d completions=%d", queue.deleted, provider.calls, completions)
	}
}

func TestQueueTerminalOrCancelledClaimStillAcknowledges(t *testing.T) {
	for _, state := range []string{"completed", "failed", "cancelled", "cancel_requested"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				// Terminal and cancelled claims deliberately carry no busy header.
				writer.WriteHeader(http.StatusConflict)
			}))
			defer server.Close()
			callback := newTestHTTPCallback(t, server)
			provider := &queueRecoveryProvider{}
			worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{}, callback, provider)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(validMessage())
			queue := &fakeQueue{}
			if err := ProcessQueueMessage(context.Background(), worker, queue, QueueMessage{Body: string(encoded), ReceiptHandle: "terminal-receipt"}); err != nil {
				t.Fatal(err)
			}
			if queue.deleted != 1 || provider.calls != 0 {
				t.Fatalf("terminal claim must ACK without inference: deleted=%d calls=%d", queue.deleted, provider.calls)
			}
		})
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

func TestRunQueueWithDrainStopsReceivingNewMessages(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "must not start"})
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	encoded, _ := json.Marshal(message)
	queue := &drainingQueue{messages: []QueueMessage{{Body: string(encoded), ReceiptHandle: "drain"}}}
	drain := make(chan struct{})
	close(drain)
	if err := RunQueueWithDrain(context.Background(), worker, queue, 1, nil, drain); err != nil {
		t.Fatal(err)
	}
	queue.mu.Lock()
	deferred := len(queue.messages)
	queue.mu.Unlock()
	if deferred != 1 {
		t.Fatalf("draining worker consumed %d messages, want 0 (deferred=%d)", 1-deferred, deferred)
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
