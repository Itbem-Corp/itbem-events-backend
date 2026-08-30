package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/uuid"
)

type fakeStore struct {
	mu       sync.Mutex
	input    []byte
	writes   map[string][]byte
	existing map[string][]byte
}

func (s *fakeStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.existing[bucket+"/"+key]; ok {
		return value, nil
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
	mu      sync.Mutex
	updates []TaskUpdate
}

func (c *fakeCallback) Update(_ context.Context, _ string, update TaskUpdate) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, update)
	return true, nil
}

type fakeProvider struct {
	completion Completion
	err        error
}

type failingResultStore struct{ input []byte }

func (s failingResultStore) Get(_ context.Context, _ string, _ string) ([]byte, error) {
	return s.input, nil
}
func (failingResultStore) PutEncryptedJSON(_ context.Context, _ string, key string, _ []byte) error {
	// The exact request reaches durable storage before the provider is called;
	// this fixture fails only response persistence to verify that accounting is
	// still reported after a billable call.
	if strings.HasSuffix(key, "/request.json") {
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

func TestWorkerWritesEncryptedPrivateResultAndCallbacks(t *testing.T) {
	input, _ := json.Marshal(TaskInput{Prompt: "Plan the scoped change", Delivery: json.RawMessage(`{"work_item":{"id":"task"}}`)})
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	plan := `{"summary":"bounded","goal_interpretation":"bounded change","confidence":0.9,"autonomy_boundary":"wait for gates","context_reviewed":[],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":[],"risks":[],"qa_plan":[],"evidence_plan":[],"acceptance_criteria":[],"repository_impact":[],"files_impacted":[],"rollback_plan":[],"estimate":"0 minutes","questions":[]}`
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, fakeProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M2.7", Content: plan, Usage: map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), validMessage()); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 2 || callback.updates[0].Status != "running" || callback.updates[1].Status != "completed" {
		t.Fatalf("unexpected callbacks: %#v", callback.updates)
	}
	if callback.updates[0].RunID == "" || callback.updates[0].RunID != callback.updates[1].RunID {
		t.Fatalf("one worker execution must hold the same opaque run lease through completion: %#v", callback.updates)
	}
	if len(store.writes) != 3 {
		t.Fatalf("expected canonical request plus immutable and recovery result writes, got %#v", store.writes)
	}
	request := store.writes["itbem-ai-outputs-local/automation/task/runs/"+callback.updates[1].RunID+"/request.json"]
	if !strings.Contains(string(request), "Plan the scoped change") || !strings.Contains(string(request), "max_completion_tokens") {
		t.Fatalf("execution must retain the canonical provider request: %s", request)
	}
	if !strings.Contains(callback.updates[1].RequestRef, "/runs/"+callback.updates[1].RunID+"/request.json") {
		t.Fatalf("execution callback must retain its immutable run request: %#v", callback.updates[1])
	}
	if !strings.Contains(callback.updates[1].OutputRef, "/runs/"+callback.updates[1].RunID+"/result.json") {
		t.Fatalf("execution callback must retain its immutable run result: %#v", callback.updates[1])
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
	if len(callback.updates) != 2 || callback.updates[1].Status != "completed" {
		t.Fatalf("code review must be stored and completed, got %#v", callback.updates)
	}
	resultRaw := store.writes["itbem-ai-outputs-local/automation/task/runs/"+callback.updates[1].RunID+"/result.json"]
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
	if len(callback.updates) != 2 || callback.updates[1].Status != "failed" || callback.updates[1].OutputRef == "" {
		t.Fatalf("expected failed callback with private result: %#v", callback.updates)
	}
	failed := callback.updates[1]
	if failed.Provider != ProviderMiniMax || failed.Model != "MiniMax-M3" || failed.ResponseID != "response-1" || failed.Usage["total_tokens"] != 7 {
		t.Fatalf("provider accounting metadata was not retained: %#v", failed)
	}
	result := store.writes["itbem-ai-outputs-local/automation/task/result.json"]
	if !strings.Contains(string(result), "not a JSON plan") || !strings.Contains(string(result), "validation_error") {
		t.Fatalf("private failure result is incomplete: %s", result)
	}
	if !strings.Contains(failed.OutputRef, "/runs/"+failed.RunID+"/result.json") {
		t.Fatalf("failed provider result must retain an immutable run reference: %#v", failed)
	}
	if !strings.Contains(failed.RequestRef, "/runs/"+failed.RunID+"/request.json") {
		t.Fatalf("failed provider result must retain its immutable request reference: %#v", failed)
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
	if len(callback.updates) != 2 || callback.updates[1].Status != "failed" || callback.updates[1].OutputRef != "" {
		t.Fatalf("expected accounting-only failed callback: %#v", callback.updates)
	}
	failed := callback.updates[1]
	if failed.Provider != ProviderMiniMax || failed.Model != "MiniMax-M3" || failed.ResponseID != "response-2" || failed.Usage["total_tokens"] != 9 {
		t.Fatalf("provider accounting was lost after storage failure: %#v", failed)
	}
}

func TestWorkerRecoveryReusesOriginalInferenceRunWithoutCreatingNewCostIdentity(t *testing.T) {
	originalRunID := uuid.Must(uuid.NewV4()).String()
	recoveryLeaseID := uuid.Must(uuid.NewV4()).String()
	result, err := json.Marshal(map[string]any{
		"schema_version": 1, "task_id": "task", "run_id": originalRunID,
		"request_ref": "s3://itbem-ai-outputs-local/automation/task/runs/" + originalRunID + "/request.json",
		"provider":    ProviderMiniMax, "model": "MiniMax-M3", "response_id": "response-original",
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
	result := map[string]any{"grant_id": "d4a4b837-2e18-43af-9f58-6d59629db2bb", "branch": "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb", "commit_sha": strings.Repeat("a", 40), "pull_request_url": "https://github.com/itbem/repo/pull/1", "token": "must-never-escape", "command_output": "private"}
	handoff, err := json.Marshal(publicationHandoff(result))
	if err != nil || strings.Contains(string(handoff), "must-never-escape") || strings.Contains(string(handoff), "command_output") {
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
	result, _ := json.Marshal(map[string]any{"schema_version": 1, "task_id": message.Payload.TaskID, "provider": "minimax", "model": "MiniMax-M2.7", "usage": map[string]any{}})
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
		"usage": map[string]any{"total_tokens": 9}, "validation_error": "delivery summary requires an executive object",
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
