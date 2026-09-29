package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

type fakeStore struct {
	mu           sync.Mutex
	input        []byte
	outputBucket string
	writes       map[string][]byte
	existing     map[string][]byte
}

func (s *fakeStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.existing[bucket+"/"+key]; ok {
		return value, nil
	}
	if value, ok := s.writes[bucket+"/"+key]; ok {
		return value, nil
	}
	if strings.HasPrefix(bucket, "itbem-ai-outputs-") || bucket == s.outputBucket {
		return nil, ErrObjectNotFound
	}
	return s.input, nil
}
func (s *fakeStore) PutEncryptedJSON(_ context.Context, bucket, key string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writes == nil {
		s.writes = map[string][]byte{}
	}
	s.writes[bucket+"/"+key] = body
	return nil
}

type fakeCallback struct {
	mu        sync.Mutex
	updates   []TaskUpdate
	operation string
}

func (c *fakeCallback) Update(_ context.Context, taskID string, update TaskUpdate) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, update)
	if update.Status == "running" && strings.TrimSpace(update.RunID) != "" {
		operation := c.operation
		if operation == "" {
			operation = "delivery.plan"
		}
		if err := issueTestInferenceCapability(taskID, update, operation); err != nil {
			return false, err
		}
	} else if update.Status == "completed" || update.Status == "failed" || update.Status == "cancelled" {
		clearInferenceCapabilitiesForRun(taskID, update.RunID)
	}
	return true, nil
}

func issueTestInferenceCapability(taskID string, update TaskUpdate, operation string) error {
	workerID, agentKey, machineID := update.WorkerID, update.AgentKey, update.MachineID
	if workerID == "" {
		workerID = "a69b7f51-58b9-4f0e-aef3-1fbc23f79826"
	}
	if agentKey == "" {
		agentKey = "generalist"
	}
	if machineID == "" {
		machineID = "b69b7f51-58b9-4f0e-aef3-1fbc23f79827"
	}
	token, err := inferencecapability.Mint("test-only-server-signing-key-never-used-by-runtime", inferencecapability.Scope{
		TaskID: taskID, RunID: update.RunID, Operation: operation,
		WorkerID: workerID, AgentKey: agentKey, MachineID: machineID,
	}, time.Minute)
	if err != nil {
		return err
	}
	return storeInferenceCapability(taskID, update.RunID, token, time.Now().UTC())
}

type fakeProvider struct {
	completion Completion
	err        error
}

type transientReadError struct{}

func (transientReadError) Error() string             { return "temporary gateway storage failure" }
func (transientReadError) RetryDelay() time.Duration { return time.Nanosecond }

type transientReadStore struct{ calls int }

func (s *transientReadStore) Get(context.Context, string, string) ([]byte, error) {
	s.calls++
	if s.calls < 3 {
		return nil, transientReadError{}
	}
	return nil, ErrObjectNotFound
}

func (*transientReadStore) PutEncryptedJSON(context.Context, string, string, []byte) error {
	return nil
}

func TestPrivatePreInferenceReadRetriesOnlyTransportClassifiedOutage(t *testing.T) {
	store := &transientReadStore{}
	worker := &Worker{store: store}
	_, err := worker.readPrivateObjectBeforeInference(context.Background(), "itbem-ai-outputs-test", "automation/task/result.json")
	if !errors.Is(err, ErrObjectNotFound) || store.calls != 3 {
		t.Fatalf("read = (%v, %d calls), want missing after two transient retries", err, store.calls)
	}
}

// capabilityProvider models an adapter that advertises a bounded contract but
// must never be called when admission rejects the requested operation.
type capabilityProvider struct {
	caps  ProviderCapabilities
	calls *int
}

func (p capabilityProvider) Capabilities() ProviderCapabilities { return p.caps }

func (p capabilityProvider) Complete(_ context.Context, _ []Message, _ int) (Completion, error) {
	if p.calls != nil {
		(*p.calls)++
	}
	return Completion{}, errors.New("provider must not run after capability rejection")
}

type failingResultStore struct{ input []byte }

func (s failingResultStore) Get(_ context.Context, bucket string, _ string) ([]byte, error) {
	if strings.HasPrefix(bucket, "itbem-ai-outputs-") {
		return nil, ErrObjectNotFound
	}
	return s.input, nil
}
func (failingResultStore) PutEncryptedJSON(_ context.Context, _ string, key string, _ []byte) error {
	// The exact request reaches durable storage before the provider is called;
	// this fixture fails only response persistence to verify that accounting is
	// still reported after a billable call.
	if strings.HasSuffix(key, "/request.json") || strings.HasSuffix(key, "/provider-intent.json") {
		return nil
	}
	return errors.New("private object storage unavailable")
}

func (p fakeProvider) Complete(_ context.Context, _ []Message, _ int) (Completion, error) {
	return p.completion, p.err
}

func validMessage() TaskMessage {
	var message TaskMessage
	message.SchemaVersion = 1
	message.JobID = "job"
	message.TenantCode = "itbem"
	message.Type = "ai.local.process"
	message.Payload.TaskID = "task"
	message.Payload.Operation = "delivery.plan"
	message.Payload.InputRef = "s3://itbem-ai-inputs-local/automation/inputs/task/input.json"
	message.Payload.Attempt = 1
	return message
}

func TestTargetedPlanStepMessageCanReachSameProfileFailoverWorker(t *testing.T) {
	worker := &Worker{config: WorkerConfig{AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()}}
	message := validMessage()
	message.Payload.Operation = "delivery.implementation"
	message.Payload.AgentKey = "generalist"
	message.Payload.PlanStepID = uuid.Must(uuid.NewV4()).String()
	message.Payload.TargetMachineID = uuid.Must(uuid.NewV4()).String() // original machine A

	if !worker.matchesTargetPlanStepAgent(message) {
		t.Fatal("same-profile worker B must receive the existing plan-step redelivery; the server validates failover")
	}

	message.Payload.AgentKey = "specialist"
	if worker.matchesTargetPlanStepAgent(message) {
		t.Fatal("a different profile must not consume the targeted plan-step message")
	}
}

func TestDeliveryGovernanceInstructionPreservesHumanAuthorityWithoutTrustingAgentMessages(t *testing.T) {
	instruction := deliveryGovernanceInstruction("delivery.plan")
	for _, required := range []string{"delivery.gates", "author_type=human", "unresolved rework", "Messages from any other author", "never opened, satisfied, or bypassed"} {
		if !strings.Contains(instruction, required) {
			t.Fatalf("delivery governance instruction lost %q: %s", required, instruction)
		}
	}
	if deliveryGovernanceInstruction("ai.chat") != "" {
		t.Fatal("non-delivery operations must not receive delivery governance semantics")
	}
}

func TestDeliveryPlanInstructionDefinesNormalizedExecutionDAG(t *testing.T) {
	messages, err := buildTaskMessages("delivery.plan", TaskInput{Prompt: "Plan the bounded work", Delivery: json.RawMessage(`{}`)}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	instruction := messages[0].Content
	for _, required := range []string{
		"EXECUTION DAG CONTRACT",
		"execution_steps is a required array",
		"step_key, order, title, objective, acceptance_criteria",
		"depends_on",
		"optional idempotency_key",
		"unique lowercase snake_case identifier",
		"unique consecutive 1-based display order only",
		"dependency must name an existing step_key",
		"dependency graph must be acyclic",
		"independent steps must not depend on each other and may run in parallel",
		"implementation_steps remains the backwards-compatible summary of titles",
		"ordered list of execution_steps.title exactly",
		"never include private reasoning or chain-of-thought",
	} {
		if !strings.Contains(instruction, required) {
			t.Fatalf("delivery planner is missing DAG/schema guidance %q", required)
		}
	}
	if !strings.Contains(instruction, "execution_steps:[], implementation_steps:[]") {
		t.Fatal("empty-context planning must emit an empty execution graph and matching title summary")
	}
}

func TestNewWorkerCreatesUniqueProcessInstanceIdentity(t *testing.T) {
	newWorker := func() *Worker {
		worker, err := NewWorker(
			WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"},
			&fakeStore{}, &fakeCallback{}, fakeProvider{},
		)
		if err != nil {
			t.Fatal(err)
		}
		return worker
	}
	first, second := newWorker().identity(), newWorker().identity()
	firstID, firstErr := uuid.FromString(first.WorkerID)
	secondID, secondErr := uuid.FromString(second.WorkerID)
	if firstErr != nil || secondErr != nil || firstID == uuid.Nil || secondID == uuid.Nil || first.WorkerID == second.WorkerID {
		t.Fatalf("workers must have distinct valid process identities: first=%#v second=%#v", first, second)
	}
}

func TestWorkerWritesEncryptedPrivateResultAndCallbacks(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "Plan the scoped change", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	plan := `{"summary":"bounded","goal_interpretation":"bounded change","confidence":0.9,"autonomy_boundary":"wait for gates","context_reviewed":[],"context_gaps":[],"assumptions":[],"human_decisions":[],"execution_steps":[{"step_key":"inspect_backend","order":1,"title":"Inspect backend","objective":"Identify the API impact","acceptance_criteria":["Affected API behavior is recorded"],"depends_on":[]},{"step_key":"inspect_frontend","order":2,"title":"Inspect frontend","objective":"Identify the UI impact","acceptance_criteria":["Affected UI behavior is recorded"],"depends_on":[]},{"step_key":"integrate_plan","order":3,"title":"Integrate the plan","objective":"Combine findings and define verification","acceptance_criteria":["Plan covers both surfaces"],"depends_on":["inspect_backend","inspect_frontend"]}],"implementation_steps":["Inspect backend","Inspect frontend","Integrate the plan"],"risks":[],"qa_plan":[],"evidence_plan":[],"acceptance_criteria":[],"repository_impact":[],"files_impacted":[],"rollback_plan":[],"estimate":"0 minutes","questions":[]}`
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: plan, Usage: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 3 || callback.updates[0].Status != "running" || callback.updates[1].Status != "running" || callback.updates[1].ProgressStep != "thinking" || callback.updates[2].Status != "completed" {
		t.Fatalf("unexpected callbacks: %#v", callback.updates)
	}
	if callback.updates[0].RunID == "" || callback.updates[0].RunID != callback.updates[1].RunID || callback.updates[1].RunID != callback.updates[2].RunID {
		t.Fatalf("one worker execution must hold the same opaque run lease through completion: %#v", callback.updates)
	}
	if callback.updates[0].WorkerID == "" || callback.updates[0].WorkerID != callback.updates[2].WorkerID {
		t.Fatalf("worker instance identity must remain stable throughout a task run: %#v", callback.updates)
	}
	if len(store.writes) != 4 {
		t.Fatalf("expected canonical request, provider intent, immutable and recovery result writes, got %#v", store.writes)
	}
	request := store.writes["itbem-ai-outputs-local/automation/task/runs/"+callback.updates[2].RunID+"/request.json"]
	if !strings.Contains(string(request), "Plan the scoped change") || !strings.Contains(string(request), "max_completion_tokens") {
		t.Fatalf("execution must retain the canonical provider request: %s", request)
	}
	if !strings.Contains(callback.updates[2].RequestRef, "/runs/"+callback.updates[2].RunID+"/request.json") {
		t.Fatalf("execution callback must retain its immutable run request: %#v", callback.updates[2])
	}
	if !strings.Contains(callback.updates[2].OutputRef, "/runs/"+callback.updates[2].RunID+"/result.json") {
		t.Fatalf("execution callback must retain its immutable run result: %#v", callback.updates[2])
	}
	var persisted struct {
		StructuredResult struct {
			ExecutionSteps []struct {
				StepKey string   `json:"step_key"`
				Order   int      `json:"order"`
				Title   string   `json:"title"`
				Depends []string `json:"depends_on"`
			} `json:"execution_steps"`
			ImplementationSteps []string `json:"implementation_steps"`
		} `json:"structured_result"`
	}
	resultKey := "itbem-ai-outputs-local/automation/task/runs/" + callback.updates[1].RunID + "/result.json"
	if err := json.Unmarshal(store.writes[resultKey], &persisted); err != nil {
		t.Fatalf("persisted plan did not retain its structured execution graph: %v", err)
	}
	steps := persisted.StructuredResult.ExecutionSteps
	if len(steps) != 3 || steps[0].StepKey != "inspect_backend" || steps[1].StepKey != "inspect_frontend" || steps[0].Order != 1 || steps[1].Order != 2 || len(steps[0].Depends) != 0 || len(steps[1].Depends) != 0 || !reflect.DeepEqual(steps[2].Depends, []string{"inspect_backend", "inspect_frontend"}) {
		t.Fatalf("persisted plan lost its normalized DAG or independent steps: %#v", steps)
	}
	if !reflect.DeepEqual(persisted.StructuredResult.ImplementationSteps, []string{"Inspect backend", "Inspect frontend", "Integrate the plan"}) {
		t.Fatalf("legacy implementation_steps must remain the ordered title summary: %#v", persisted.StructuredResult.ImplementationSteps)
	}
}

func TestWorkerRejectsIncompatibleProviderBeforeInferenceOrIntent(t *testing.T) {
	input, err := json.Marshal(TaskInput{Prompt: "Plan the scoped change", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	calls := 0
	provider := capabilityProvider{
		calls: &calls,
		caps: ProviderCapabilities{
			ContractVersion: 1, Provider: ProviderMiniMax, Model: "MiniMax-M3",
			SupportsJSONActions: true, SupportsUsageLedger: true,
			SupportsCancellation: true, SupportsRequestAudit: true,
			MaxCompletionTokens: 1, MaxRequestBytes: AgentMaxRequestBytes,
		},
	}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", RequireProviderCapabilities: true}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("incompatible provider must be rejected before inference, got %d calls", calls)
	}
	if len(callback.updates) != 2 || callback.updates[0].Status != "running" || callback.updates[1].Status != "failed" {
		t.Fatalf("capability admission must leave an explicit terminal callback: %#v", callback.updates)
	}
	if !strings.Contains(callback.updates[1].ErrorMessage, "cannot satisfy") {
		t.Fatalf("callback must expose an actionable capability error: %#v", callback.updates[1])
	}
	for key := range store.writes {
		if strings.HasSuffix(key, "/request.json") || strings.HasSuffix(key, "/provider-intent.json") {
			t.Fatalf("capability rejection must not persist an inference request or intent: %s", key)
		}
	}
}

func TestWorkerRejectsUnregisteredProviderWhenRuntimeRequiresCapabilities(t *testing.T) {
	input, err := json.Marshal(TaskInput{Prompt: "Plan the scoped change", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{
		InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", RequireProviderCapabilities: true,
	}, store, callback, fakeProvider{err: errors.New("provider must not run")})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 2 || callback.updates[1].Status != "failed" || !strings.Contains(callback.updates[1].ErrorMessage, "capability contract") {
		t.Fatalf("runtime must fail closed for an unregistered provider: %#v", callback.updates)
	}
	if len(store.writes) != 0 {
		t.Fatalf("unregistered provider must not create private inference artifacts: %#v", store.writes)
	}
}

func TestWorkerPersistsConservativeCoverageSignalForCodeReview(t *testing.T) {
	input, err := json.Marshal(TaskInput{Prompt: "Review the frozen pull request.", Delivery: json.RawMessage(validCodeReviewInput())})
	if err != nil {
		t.Fatal(err)
	}
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	completion := `{"summary":"The changed handler is internally consistent.","verdict":"approve","review_scope":["handler"],"findings":[],"test_plan":["Run the handler test suite."],"coverage_gaps":[]}`
	worker, err := NewWorker(
		WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback,
		fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: completion, ResponseID: "review-response", Usage: map[string]any{"total_tokens": 11}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "code.review"
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 3 || callback.updates[2].Status != "completed" {
		t.Fatalf("code review must be stored and completed, got %#v", callback.updates)
	}
	resultRaw := store.writes["itbem-ai-outputs-local/automation/task/runs/"+callback.updates[2].RunID+"/result.json"]
	var result struct {
		StructuredResult map[string]any `json:"structured_result"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatal(err)
	}
	if result.StructuredResult["verdict"] != "comment" {
		t.Fatalf("diff-only review result must not preserve an unqualified approval: %#v", result.StructuredResult)
	}
	gaps, ok := result.StructuredResult["coverage_gaps"].([]any)
	if !ok || len(gaps) != 1 || !strings.Contains(gaps[0].(string), "No test change") {
		t.Fatalf("persisted review must surface the deterministic coverage gap: %#v", result.StructuredResult)
	}
}

func TestWorkerRetainsInvalidDeliveryPlanForAuthorizedInspectionAndLedger(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "Plan the scoped change", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	worker, err := NewWorker(
		WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"},
		store,
		callback,
		fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: "not a JSON plan", ResponseID: "response-1", Usage: map[string]any{"total_tokens": 7}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 3 || callback.updates[2].Status != "failed" || callback.updates[2].OutputRef == "" {
		t.Fatalf("expected failed callback with private result: %#v", callback.updates)
	}
	failed := callback.updates[2]
	if failed.Provider != ProviderMiniMax || failed.Model != "MiniMax-M3" || failed.ResponseID != "response-1" || failed.Usage["total_tokens"] != 7 {
		t.Fatalf("provider accounting metadata was not retained: %#v", failed)
	}
	result := store.writes["itbem-ai-outputs-local/automation/task/result.json"]
	if !strings.Contains(string(result), omittedProviderResponse) || !strings.Contains(string(result), "validation_error") || strings.Contains(string(result), "not a JSON plan") {
		t.Fatalf("private failure result is incomplete: %s", result)
	}
	if !strings.Contains(failed.OutputRef, "/runs/"+failed.RunID+"/result.json") {
		t.Fatalf("failed provider result must retain an immutable run reference: %#v", failed)
	}
	if !strings.Contains(failed.RequestRef, "/runs/"+failed.RunID+"/request.json") {
		t.Fatalf("failed provider result must retain its immutable request reference: %#v", failed)
	}
}

func TestWorkerRedactsProviderContentBeforeSuccessfulPersistenceAndCallback(t *testing.T) {
	secret := "ghp_" + strings.Repeat("B", 36)
	input, err := json.Marshal(TaskInput{Prompt: "Answer the user", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	completion := Completion{
		Provider: ProviderMiniMax, Model: "MiniMax-M3",
		Content: `{"message":"Safe answer","summary":"Visible summary","analysis":"PRIVATE_ANALYSIS_CANARY","nested":{"reasoning_content":"PRIVATE_REASONING_CANARY","api_key":"` + secret + `"}}`,
		Usage:   map[string]any{"prompt_tokens": 5, "reasoning_tokens": 3, "api_key": secret},
	}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{completion: completion})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) == 0 || callback.updates[len(callback.updates)-1].Status != "completed" {
		t.Fatalf("expected successful completion callback, got %#v", callback.updates)
	}
	resultKey := "itbem-ai-outputs-local/automation/task/runs/" + callback.updates[len(callback.updates)-1].RunID + "/result.json"
	resultBytes := store.writes[resultKey]
	if len(resultBytes) == 0 {
		t.Fatalf("successful run result was not persisted at %s", resultKey)
	}
	for key, body := range store.writes {
		if strings.Contains(string(body), secret) || strings.Contains(string(body), "PRIVATE_ANALYSIS_CANARY") || strings.Contains(string(body), "PRIVATE_REASONING_CANARY") {
			t.Fatalf("provider canary was persisted in %s: %s", key, body)
		}
	}
	var result map[string]any
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatal(err)
	}
	content, _ := result["content"].(string)
	var safeContent map[string]any
	if err := json.Unmarshal([]byte(content), &safeContent); err != nil {
		t.Fatal(err)
	}
	if safeContent["message"] != "Safe answer" || safeContent["summary"] != "Visible summary" || safeContent["analysis"] != nil {
		t.Fatalf("safe operation JSON was not preserved or private analysis was retained: %#v", safeContent)
	}
	last := callback.updates[len(callback.updates)-1]
	encodedCallback, _ := json.Marshal(callback.updates)
	if strings.Contains(string(encodedCallback), secret) || strings.Contains(string(encodedCallback), "PRIVATE_") || last.Usage["reasoning_tokens"] != 3 {
		t.Fatalf("callback leaked a canary or discarded the token metric: %s; update=%#v", encodedCallback, last)
	}
}

func TestWorkerRedactsProviderResponseErrorBeforeFailurePersistenceAndCallback(t *testing.T) {
	secret := "ghp_" + strings.Repeat("C", 36)
	input, err := json.Marshal(TaskInput{Prompt: "Generate the plan", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	providerErr := &ProviderResponseError{
		Completion: Completion{
			Provider: ProviderMiniMax, Model: "MiniMax-M3",
			Content: `{"summary":"Safe provider failure summary","analysis":"PRIVATE_ANALYSIS_CANARY","reasoning":"PRIVATE_REASONING_CANARY","api_key":"` + secret + `"}`,
			Usage:   map[string]any{"prompt_tokens": 8, "reasoning_tokens": 2, "api_key": secret},
		},
		Message: "Provider rejected response; token=" + secret,
	}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{err: providerErr})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) == 0 || callback.updates[len(callback.updates)-1].Status != "failed" || callback.updates[len(callback.updates)-1].OutputRef == "" {
		t.Fatalf("expected failed provider-result callback with persisted output, got %#v", callback.updates)
	}
	resultKey := "itbem-ai-outputs-local/automation/task/runs/" + callback.updates[len(callback.updates)-1].RunID + "/result.json"
	resultBytes := store.writes[resultKey]
	if len(resultBytes) == 0 {
		t.Fatalf("failed provider response was not persisted at %s", resultKey)
	}
	for key, body := range store.writes {
		if strings.Contains(string(body), secret) || strings.Contains(string(body), "PRIVATE_ANALYSIS_CANARY") || strings.Contains(string(body), "PRIVATE_REASONING_CANARY") {
			t.Fatalf("failed provider canary was persisted in %s: %s", key, body)
		}
	}
	var result map[string]any
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatal(err)
	}
	content, _ := result["content"].(string)
	if !strings.Contains(content, "Safe provider failure summary") || strings.Contains(content, "PRIVATE_") {
		t.Fatalf("failed provider result did not preserve its safe summary or removed private content: %s", resultBytes)
	}
	encodedCallback, _ := json.Marshal(callback.updates)
	if strings.Contains(string(encodedCallback), secret) || strings.Contains(string(encodedCallback), "PRIVATE_") {
		t.Fatalf("provider response error callback leaked a private canary: %s", encodedCallback)
	}
}

func TestWorkerRetainsProviderAccountingWhenResultStorageFails(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "Plan the scoped change", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	plan := `{"summary":"bounded","goal_interpretation":"bounded change","confidence":0.9,"autonomy_boundary":"wait for gates","context_reviewed":[],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":[],"risks":[],"qa_plan":[],"evidence_plan":[],"acceptance_criteria":[],"repository_impact":[],"files_impacted":[],"rollback_plan":[],"estimate":"0 minutes","questions":[]}`
	callback := &fakeCallback{}
	worker, err := NewWorker(
		WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"},
		failingResultStore{input: input}, callback,
		fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: plan, ResponseID: "response-2", Usage: map[string]any{"total_tokens": 9}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 3 || callback.updates[2].Status != "failed" || callback.updates[2].OutputRef != "" {
		t.Fatalf("expected accounting-only failed callback: %#v", callback.updates)
	}
	failed := callback.updates[2]
	if failed.Provider != ProviderMiniMax || failed.Model != "MiniMax-M3" || failed.ResponseID != "response-2" || failed.Usage["total_tokens"] != 9 {
		t.Fatalf("provider accounting was lost after storage failure: %#v", failed)
	}
}

type countingProvider struct {
	completion Completion
	calls      int
}

func (p *countingProvider) Complete(_ context.Context, _ []Message, _ int) (Completion, error) {
	p.calls++
	return p.completion, nil
}

type failTerminalCallback struct {
	updates       []TaskUpdate
	failFirstTerm bool
}

func (c *failTerminalCallback) Update(_ context.Context, taskID string, update TaskUpdate) (bool, error) {
	c.updates = append(c.updates, update)
	if update.Status == "running" {
		if err := issueTestInferenceCapability(taskID, update, "delivery.chat"); err != nil {
			return false, err
		}
	}
	if update.Status == "failed" && c.failFirstTerm {
		c.failFirstTerm = false
		return false, errors.New("callback transport unavailable")
	}
	return true, nil
}

type redeliveryStore struct {
	input       []byte
	objects     map[string][]byte
	failResults bool
}

func (s *redeliveryStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if value, ok := s.objects[bucket+"/"+key]; ok {
		return value, nil
	}
	if strings.HasPrefix(bucket, "itbem-ai-outputs-") {
		return nil, ErrObjectNotFound
	}
	return s.input, nil
}

func (s *redeliveryStore) PutEncryptedJSON(_ context.Context, bucket, key string, body []byte) error {
	if s.failResults && strings.HasSuffix(key, "/result.json") {
		return errors.New("private result storage unavailable")
	}
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[bucket+"/"+key] = append([]byte(nil), body...)
	return nil
}

func TestWorkerRedeliveryWithProviderIntentFailsClosedWithoutRepeatingInference(t *testing.T) {
	input, err := json.Marshal(TaskInput{Prompt: "Answer the bounded question", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "delivery.chat"
	provider := &countingProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", ResponseID: "uncertain-response", Content: `{"answer":"durable answer","next_steps":[],"questions":[]}`, Usage: map[string]any{"total_tokens": 9}}}
	store := &redeliveryStore{input: input, objects: map[string][]byte{}, failResults: true}
	callback := &failTerminalCallback{failFirstTerm: true}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), message); err == nil {
		t.Fatal("lost terminal callback must retain the queue delivery")
	}
	if provider.calls != 1 {
		t.Fatalf("initial delivery provider calls=%d, want 1", provider.calls)
	}
	if _, ok := store.objects["itbem-ai-outputs-local/automation/task/provider-intent.json"]; !ok {
		t.Fatal("provider intent was not persisted before inference")
	}
	store.failResults = false
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("redelivery repeated an uncertain provider call: %d", provider.calls)
	}
	last := callback.updates[len(callback.updates)-1]
	if last.Status != "failed" || !strings.Contains(last.ErrorMessage, "outcome uncertain") {
		t.Fatalf("redelivery did not surface explicit uncertainty: %#v", last)
	}
}

func TestWorkerRecoveryReusesOriginalInferenceRunWithoutCreatingNewCostIdentity(t *testing.T) {
	originalRunID := uuid.Must(uuid.NewV4()).String()
	recoveryLeaseID := uuid.Must(uuid.NewV4()).String()
	result, err := json.Marshal(map[string]any{
		"schema_version": 1, "task_id": "task", "run_id": originalRunID,
		"request_ref": "s3://itbem-ai-outputs-local/automation/task/runs/" + originalRunID + "/request.json",
		"provider":    ProviderMiniMax, "model": "MiniMax-M3", "response_id": "response-original",
		"call_id": uuid.Must(uuid.NewV4()).String(), "receipt_id": uuid.Must(uuid.NewV4()).String(),
		"usage": map[string]any{"total_tokens": 12}, "artifacts": map[string]any{"artifacts": []any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{existing: map[string][]byte{
		"itbem-ai-outputs-local/automation/task/result.json": result,
	}}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := worker.completeFromExistingResult(context.Background(), "task", recoveryLeaseID)
	if err != nil || !reused || len(callback.updates) != 1 {
		t.Fatalf("stored provider result should be recovered once: %v / %v / %#v", reused, err, callback.updates)
	}
	update := callback.updates[0]
	if update.RunID != recoveryLeaseID || update.RecoveryRunID != originalRunID || !strings.Contains(update.RequestRef, "/runs/"+originalRunID+"/request.json") || !strings.Contains(update.OutputRef, "/runs/"+originalRunID+"/result.json") {
		t.Fatalf("recovery must retain original billable evidence while using the new lease: %#v", update)
	}
}

func TestWorkerRecoveryAllowsFreshInferenceWhenGatewayArtifactsAreAbsent(t *testing.T) {
	store := &fakeStore{}
	worker, err := NewWorker(
		WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"},
		store,
		&fakeCallback{},
		fakeProvider{},
	)
	if err != nil {
		t.Fatal(err)
	}

	reused, err := worker.completeFromExistingResult(context.Background(), "task", uuid.Must(uuid.NewV4()).String())
	if err != nil {
		t.Fatalf("gateway not-found response must not be treated as a storage outage: %v", err)
	}
	if reused {
		t.Fatal("absent result and provider intent were incorrectly treated as recovered output")
	}
}

func TestPublicationUncertaintyNeverRecoversAsCompleted(t *testing.T) {
	store := &fakeStore{existing: map[string][]byte{}}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	originalRunID := uuid.Must(uuid.NewV4()).String()
	recoveryRunID := uuid.Must(uuid.NewV4()).String()
	effect := &PublicationEffectError{Partial: map[string]any{
		"grant_id": "d4a4b837-2e18-43af-9f58-6d59629db2bb", "branch": "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb",
		"commit_sha": strings.Repeat("a", 40),
	}, Cause: errors.New("remote connection closed after push")}
	if err := worker.failWithPublicationEffect(context.Background(), "task", originalRunID, effect); err != nil {
		t.Fatalf("accepted uncertain publication callback should settle the queue message: %v", err)
	}
	if len(callback.updates) != 1 || callback.updates[0].Status != "failed" || callback.updates[0].OutputRef == "" {
		t.Fatalf("uncertain publication callback missing: %#v", callback.updates)
	}
	result := store.writes["itbem-ai-outputs-local/automation/task/result.json"]
	if !strings.Contains(string(result), `"effect_state":"uncertain"`) || !strings.Contains(string(result), `"reconciliation_required":true`) {
		t.Fatalf("uncertainty evidence was not durable: %s", result)
	}
	store.existing["itbem-ai-outputs-local/automation/task/result.json"] = result
	callback.updates = nil
	reused, err := worker.completeFromExistingResult(context.Background(), "task", recoveryRunID)
	if err != nil || !reused || len(callback.updates) != 1 {
		t.Fatalf("uncertain publication recovery failed: reused=%v err=%v updates=%#v", reused, err, callback.updates)
	}
	if callback.updates[0].Status != "failed" || !strings.Contains(callback.updates[0].ErrorMessage, "uncertain") || callback.updates[0].Status == "completed" {
		t.Fatalf("uncertain publication became gate-eligible: %#v", callback.updates[0])
	}
}

func TestPartialQAFailureStoresPrivateDiagnosticWithoutOpeningGate(t *testing.T) {
	store := &fakeStore{}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	qaResult := map[string]any{
		"repository_execution_order": []string{"workspace://api", "workspace://dashboard"},
		"repository_runs":            []any{map[string]any{"workspace": "workspace://api", "commands": []any{map[string]any{"passed": true}}}},
	}
	if err := worker.failWithQAResult(context.Background(), "task", uuid.Must(uuid.NewV4()).String(), qaResult, nil, errors.New("dashboard QA tool timed out")); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 1 || callback.updates[0].Status != "failed" || callback.updates[0].OutputRef == "" || callback.updates[0].Execution["partial"] != true {
		t.Fatalf("partial QA must remain a durable failed diagnostic: %#v", callback.updates)
	}
	var stored map[string]any
	if err := json.Unmarshal(store.writes["itbem-ai-outputs-local/automation/task/result.json"], &stored); err != nil {
		t.Fatal(err)
	}
	if stored["validation_error"] != "dashboard QA tool timed out" || stored["provider"] != nil {
		t.Fatalf("partial QA result must not invent provider accounting: %#v", stored)
	}
	if execution, ok := stored["execution"].(map[string]any); !ok || execution["partial"] != true {
		t.Fatalf("partial execution marker was not persisted: %#v", stored["execution"])
	}
}

func TestImplementationHandoffDoesNotCopyCommandOutput(t *testing.T) {
	result := map[string]any{
		"workspace":          "workspace://backend",
		"worktree":           "workspace://backend#itbem-agent/123",
		"branch":             "itbem-agent/123",
		"github_repository":  "itbem-corp/itbem-events-backend",
		"review_diff_sha256": strings.Repeat("a", 64),
		"diff_check_passed":  true,
		"diff_stat":          "private diff metadata",
		"validations": []map[string]any{
			{"passed": true, "output": "potentially sensitive test output"},
			{"passed": false, "output": "another output"},
		},
	}
	handoff := implementationHandoff(result)
	encoded, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || string(encoded) == string(mustJSON(t, result)) {
		t.Fatal("handoff must be a reduced representation")
	}
	if string(encoded) == "" || strings.Contains(string(encoded), "output") || strings.Contains(string(encoded), "private diff") {
		t.Fatalf("handoff leaked result output: %s", encoded)
	}
	validations, ok := handoff["validations"].([]map[string]bool)
	if !ok || len(validations) != 2 || !validations[0]["passed"] || validations[1]["passed"] {
		t.Fatalf("handoff lost validation statuses: %#v", handoff)
	}
}

func TestImplementationHandoffKeepsMultiRepositoryReviewMetadataSeparate(t *testing.T) {
	result := map[string]any{"repository_execution_order": []string{"workspace://backend", "workspace://dashboard"}, "change_sets": []any{
		map[string]any{"workspace": "workspace://backend", "worktree": "workspace://backend#itbem-agent/123", "branch": "itbem-agent/123", "base_sha": strings.Repeat("a", 40), "github_repository": "itbem-corp/backend", "review_diff_sha256": strings.Repeat("b", 64), "diff_check_passed": true, "validations": []map[string]any{{"passed": true, "output": "private"}}},
		map[string]any{"workspace": "workspace://dashboard", "worktree": "workspace://dashboard#itbem-agent/123", "branch": "itbem-agent/123", "base_sha": strings.Repeat("c", 40), "github_repository": "itbem-corp/dashboard", "review_diff_sha256": strings.Repeat("d", 64), "diff_check_passed": true, "validations": []map[string]any{{"passed": true, "output": "private"}}},
	}}
	handoff := implementationHandoff(result)
	encoded, err := json.Marshal(handoff)
	if err != nil || strings.Contains(string(encoded), "private") {
		t.Fatalf("multi-repository handoff leaked private command output: %s / %v", encoded, err)
	}
	changeSets, ok := handoff["change_sets"].([]map[string]any)
	if !ok || len(changeSets) != 2 || changeSets[0]["workspace"] != "workspace://backend" || changeSets[1]["workspace"] != "workspace://dashboard" {
		t.Fatalf("multi-repository handoff lost review boundaries: %#v", handoff)
	}
	if order, ok := handoff["repository_execution_order"].([]string); !ok || !reflect.DeepEqual(order, []string{"workspace://backend", "workspace://dashboard"}) {
		t.Fatalf("multi-repository handoff lost the reviewed dependency order: %#v", handoff)
	}
}

func TestPublicationHandoffExcludesSensitiveExecutionOutput(t *testing.T) {
	result := map[string]any{"grant_id": "d4a4b837-2e18-43af-9f58-6d59629db2bb", "branch": "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb", "base_sha": strings.Repeat("b", 40), "review_diff_sha256": strings.Repeat("c", 64), "commit_sha": strings.Repeat("a", 40), "pull_request_url": "https://github.com/itbem/repo/pull/1", "token": "must-never-escape", "command_output": "private"}
	handoff, err := json.Marshal(publicationHandoff(result))
	if err != nil || !strings.Contains(string(handoff), "review_diff_sha256") || strings.Contains(string(handoff), "must-never-escape") || strings.Contains(string(handoff), "command_output") {
		t.Fatalf("publication handoff leaked sensitive execution data: %s / %v", handoff, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestWorkerRetainsRetryableProviderFailures(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "hello"})
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, &fakeCallback{}, fakeProvider{err: &RetryableError{Message: "rate limited"}})
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	err = worker.Process(context.Background(), message)
	var target *RetryableError
	if !errors.As(err, &target) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestWorkerRejectsCrossProductReferences(t *testing.T) {
	message := validMessage()
	message.TenantCode = "eventiapp"
	if err := ValidateMessage(message, "itbem-ai-inputs-local"); err == nil {
		t.Fatal("expected cross-product message rejection")
	}
}

func TestWorkerReusesPersistedResultInsteadOfReexecutingProvider(t *testing.T) {
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	result, _ := json.Marshal(map[string]any{
		"schema_version": 1, "task_id": message.Payload.TaskID, "provider": "minimax", "model": "MiniMax-M2.7", "usage": map[string]any{},
		"call_id": uuid.Must(uuid.NewV4()).String(), "receipt_id": uuid.Must(uuid.NewV4()).String(),
	})
	store := &fakeStore{existing: map[string][]byte{"itbem-ai-outputs-local/automation/task/result.json": result}}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{err: errors.New("provider must not run")})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 2 || callback.updates[1].Status != "completed" {
		t.Fatalf("expected callback-only retry: %#v", callback.updates)
	}
}

func TestWorkerRetryKeepsPersistedValidationFailureTerminal(t *testing.T) {
	message := validMessage()
	message.Payload.Operation = "delivery.summary"
	result, _ := json.Marshal(map[string]any{
		"schema_version": 1, "task_id": message.Payload.TaskID, "provider": "minimax", "model": "MiniMax-M3",
		"usage": map[string]any{"total_tokens": 9}, "call_id": uuid.Must(uuid.NewV4()).String(), "receipt_id": uuid.Must(uuid.NewV4()).String(),
		"validation_error": "delivery summary requires an executive object",
	})
	store := &fakeStore{existing: map[string][]byte{"itbem-ai-outputs-local/automation/task/result.json": result}}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{err: errors.New("provider must not run")})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 2 || callback.updates[1].Status != "failed" || !strings.Contains(callback.updates[1].ErrorMessage, "executive") {
		t.Fatalf("a persisted contract failure must remain failed: %#v", callback.updates)
	}
}

func TestWorkerRetryReplaysPersistedQAArtifactsToControlPlane(t *testing.T) {
	message := validMessage()
	message.Payload.Operation = "delivery.qa"
	artifact := ArtifactReference{Name: "01-preview.png", Reference: "s3://itbem-ai-outputs-local/automation/task/artifacts/01-preview.png", ContentType: "image/png", SizeBytes: 42, SHA256: strings.Repeat("a", 64)}
	result, _ := json.Marshal(map[string]any{
		"schema_version": 1,
		"task_id":        message.Payload.TaskID,
		"provider":       "minimax",
		"model":          "MiniMax-M3",
		"usage":          map[string]any{},
		"call_id":        uuid.Must(uuid.NewV4()).String(),
		"receipt_id":     uuid.Must(uuid.NewV4()).String(),
		"artifacts":      map[string]any{"artifacts": []ArtifactReference{artifact}},
	})
	store := &fakeStore{existing: map[string][]byte{"itbem-ai-outputs-local/automation/task/result.json": result}}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{err: errors.New("provider must not run")})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 2 || len(callback.updates[1].Artifacts) != 1 || callback.updates[1].Artifacts[0] != artifact {
		t.Fatalf("expected persisted QA artifact in replay callback: %#v", callback.updates)
	}
}

func TestDeliveryChatInstructionUsesHumanLabelsWithoutGrantingAuthority(t *testing.T) {
	messages, err := buildTaskMessages("delivery.chat", TaskInput{Prompt: "What gate is pending?", Delivery: json.RawMessage(`{}`)}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) == 0 {
		t.Fatal("expected a delivery chat system instruction")
	}
	for _, required := range []string{"human-readable labels", "raw internal action id", "human still has to make it"} {
		if !strings.Contains(messages[0].Content, required) {
			t.Fatalf("delivery chat instruction lost %q: %s", required, messages[0].Content)
		}
	}
}
