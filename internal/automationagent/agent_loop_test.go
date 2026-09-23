package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type checkpointStore struct {
	objects map[string][]byte
	readErr error
}

func (s *checkpointStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	raw, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return raw, nil
}
func (s *checkpointStore) PutEncryptedJSON(_ context.Context, bucket, key string, body []byte) error {
	if s.objects == nil {
		s.objects = map[string][]byte{}
	}
	s.objects[bucket+"/"+key] = append([]byte(nil), body...)
	return nil
}

type loopSequenceProvider struct {
	responses []string
	calls     int
}

func (p *loopSequenceProvider) Complete(_ context.Context, _ []Message, _ int) (Completion, error) {
	p.calls++
	if p.calls > len(p.responses) {
		return Completion{}, errors.New("unexpected provider call")
	}
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: p.responses[p.calls-1], Usage: map[string]any{"prompt_tokens": 10, "completion_tokens": 20}, ResponseID: fmt.Sprint(p.calls)}, nil
}

func agentFixture(t *testing.T) (*Worker, *checkpointStore, *loopSequenceProvider, *fakeCallback, TaskMessage, TaskInput, agentCheckpoint) {
	t.Helper()
	store := &checkpointStore{objects: map[string][]byte{}}
	provider := &loopSequenceProvider{}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-test", OutputBucket: "itbem-ai-outputs-test"}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := TaskMessage{}
	message.Payload.TaskID = "d4a4b837-2e18-43af-9f58-6d59629db2bb"
	message.Payload.Operation = "delivery.implementation"
	input := TaskInput{AgentExecution: true, Prompt: "implement", Delivery: json.RawMessage(`{"approved_plan":{"files_impacted":["note.go"]},"work_item":{"acceptance_criteria":["Value is two"]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`)}
	raw, _ := json.Marshal(input)
	cp := agentCheckpoint{TaskID: message.Payload.TaskID, InputDigest: fmt.Sprintf("%x", sha256.Sum256(raw)), RunID: "9f5d9d66-e58d-4357-8869-83f09bca8222", Started: time.Now().UTC(), Messages: []Message{{Role: "user", Content: "implement"}}}
	return worker, store, provider, callback, message, input, cp
}
func saveTestCheckpoint(t *testing.T, w *Worker, s *checkpointStore, cp agentCheckpoint) {
	t.Helper()
	raw, _ := json.Marshal(cp)
	if err := s.PutEncryptedJSON(context.Background(), w.config.OutputBucket, "automation/"+cp.TaskID+"/agent-checkpoint.json", raw); err != nil {
		t.Fatal(err)
	}
}
func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	result, err := runLocal(context.Background(), root, time.Minute, "", "git", args...)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("git %v: %#v %v", args, result, err)
	}
	return strings.TrimSpace(result.Output)
}
func testRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init")
	testGit(t, root, "config", "user.email", "agent@example.invalid")
	testGit(t, root, "config", "user.name", "Agent tests")
	return root
}

func TestAgentRepairsFailedValidationAndAccountsEveryCall(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	root := testRepository(t)
	files := map[string]string{"go.mod": "module agentfixture\n\ngo 1.25.0\n", "note.go": "package fixture\nvar Value = 0\n", "note_test.go": "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T){if Value!=2{t.Fatal(\"Value must be two\")}}\n"}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	config, _ := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root, ValidationCommands: [][]string{{"go", "test", "./..."}}, AcceptanceChecks: []AcceptanceCheck{{Criterion: "Value is two", Command: []string{"go", "test", "./..."}}}}})
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(config))
	for i := 0; i < 2; i++ {
		raw, _ := json.Marshal(map[string]string{"action": "patch", "summary": "change value", "patch": fmt.Sprintf("diff --git a/note.go b/note.go\n--- a/note.go\n+++ b/note.go\n@@ -1,2 +1,2 @@\n package fixture\n-var Value = %d\n+var Value = %d\n", i, i+1)})
		p.responses = append(p.responses, string(raw))
	}
	saveTestCheckpoint(t, w, s, cp)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != 2 || last.Status != "completed" || len(last.ToolExecutions) != 1 || last.Execution == nil {
		t.Fatalf("calls=%d update=%+v", p.calls, last)
	}
	body, _ := os.ReadFile(filepath.Join(root, "note.go"))
	if !strings.Contains(string(body), "Value = 0") {
		t.Fatal("base checkout changed")
	}
	if err := w.processImplementationAgent(context.Background(), message, input, "d436bcfd-b22d-4090-a462-b4ed841e521a"); err != nil {
		t.Fatal(err)
	}
	if p.calls != 2 {
		t.Fatal("recovery repeated inference")
	}
	last = callback.updates[len(callback.updates)-1]
	if last.RecoveryRunID != cp.RunID || last.Status != "completed" {
		t.Fatalf("recovered update %+v", last)
	}
}

func TestImplementationAgentRejectsUnregisteredProviderBeforeInference(t *testing.T) {
	w, store, provider, callback, message, _, checkpoint := agentFixture(t)
	w.config.RequireProviderCapabilities = true
	saveTestCheckpoint(t, w, store, checkpoint)

	if err := w.processImplementationAgent(context.Background(), message, TaskInput{
		AgentExecution: true,
		Prompt:         "implement",
		Delivery:       json.RawMessage(`{"approved_plan":{"files_impacted":["note.go"]},"work_item":{"acceptance_criteria":["Value is two"]},"context_sources":[{"kind":"repository","reference":"workspace://repo"}]}`),
	}, checkpoint.RunID); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatalf("runtime capability admission must stop implementation before inference, got %d calls", provider.calls)
	}
	last := callback.updates[len(callback.updates)-1]
	if last.Status != "failed" || !strings.Contains(last.ErrorMessage, "capability contract") {
		t.Fatalf("unregistered implementation provider must fail clearly: %#v", last)
	}
	if _, ok := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+checkpoint.RunID+"/request.json"]; ok {
		t.Fatal("unregistered implementation provider must not persist a billable request")
	}
}

func TestFinishImplementationAgentKeepsPartialEvidenceFailed(t *testing.T) {
	w, store, _, callback, message, _, cp := agentFixture(t)
	cp.Calls = []agentCall{{Completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: "{}", Usage: map[string]any{"total_tokens": 1}, ResponseID: "partial"}, RequestRef: "request", ResponseRef: "response"}}
	cp.PartialResult = map[string]any{
		"partial":                true,
		"completed_repositories": []string{"workspace://api"},
		"failed_repository":      "workspace://web",
	}
	cp.Failure = "workspace://web could not be reconciled"
	if err := w.finishImplementationAgent(context.Background(), message, cp.RunID, cp); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) == 0 || callback.updates[len(callback.updates)-1].Status != "failed" {
		t.Fatalf("partial implementation must remain failed: %#v", callback.updates)
	}
	if callback.updates[len(callback.updates)-1].Execution != nil {
		t.Fatal("partial implementation must not expose a successful execution handoff")
	}
	raw := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+cp.RunID+"/result.json"]
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	artifacts, _ := output["artifacts"].(map[string]any)
	implementation, _ := artifacts["partial_implementation"].(map[string]any)
	if implementation["partial"] != true {
		t.Fatalf("private partial evidence missing: %#v", output)
	}
}

func TestAgentAmbiguousCallDoesNotRepeatInference(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	cp.Pending = true
	saveTestCheckpoint(t, w, s, cp)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != 0 || last.Status != "failed" || !strings.Contains(last.ErrorMessage, "uncertain") {
		t.Fatalf("unexpected %+v; calls %d", last, p.calls)
	}
}

func TestAgentRecoversDurableResponseBeforeExecutingItsAction(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	cp.Pending = true
	saveTestCheckpoint(t, w, s, cp)
	completion := Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"action":"blocked","reason":"needs authorization"}`, Usage: map[string]any{"input_tokens": 10, "output_tokens": 20}}
	raw, _ := json.Marshal(map[string]any{"completion": completion, "rejected": false})
	s.objects[w.config.OutputBucket+"/automation/"+cp.TaskID+"/runs/"+cp.RunID+"/response.json"] = raw
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != 0 || last.Status != "failed" || last.Usage == nil || !strings.Contains(last.ErrorMessage, "assistance") {
		t.Fatalf("unexpected response recovery: %+v calls %d", last, p.calls)
	}
}

func TestAgentMissingAcceptanceConfigurationFailsBeforeInference(t *testing.T) {
	w, _, p, callback, message, input, cp := agentFixture(t)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	if p.calls != 0 || callback.updates[len(callback.updates)-1].Status != "failed" {
		t.Fatal("unconfigured task must not spend")
	}
}

func TestCommandBufferBoundsMemoryWithoutShortWrites(t *testing.T) {
	var buffer boundedCommandBuffer
	input := []byte(strings.Repeat("x", maxCommandOutput*3))
	n, err := buffer.Write(input)
	if err != nil || n != len(input) || buffer.Len() != maxCommandOutput {
		t.Fatalf("buffer n=%d size=%d err=%v", n, buffer.Len(), err)
	}
}
func TestAgentStorageOutageDoesNotBecomeFreshRun(t *testing.T) {
	w, s, p, _, message, input, cp := agentFixture(t)
	s.readErr = errors.New("storage down")
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err == nil || p.calls != 0 {
		t.Fatal("storage failure must stop inference")
	}
}
func TestAgentCallBudgetAndFailureLedger(t *testing.T) {
	w, s, p, callback, message, input, cp := agentFixture(t)
	for i := 0; i < AgentMaxCalls; i++ {
		p.responses = append(p.responses, `{"action":"unknown"}`)
	}
	saveTestCheckpoint(t, w, s, cp)
	if err := w.processImplementationAgent(context.Background(), message, input, cp.RunID); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if p.calls != AgentMaxCalls || last.Status != "failed" || len(last.ToolExecutions) != AgentMaxCalls-1 {
		t.Fatalf("unexpected calls %d, update %+v", p.calls, last)
	}
}
func TestAgentPatchCannotExpandApprovedFiles(t *testing.T) {
	delivery := json.RawMessage(`{"approved_plan":{"files_impacted":["note.go"]}}`)
	for _, path := range []string{"other.go", "../note.go", ".env", ".git/config"} {
		raw, _ := json.Marshal(ChangeProposal{Summary: "change", Patch: "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n"})
		if validateAgentPatchScope(delivery, string(raw)) == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
func TestReviewedDiffDigestIncludesTailBeyondLogLimit(t *testing.T) {
	root := testRepository(t)
	path := filepath.Join(root, "large.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	base := testGit(t, root, "rev-parse", "HEAD")
	prefix := strings.Repeat("long changed line to exceed truncated command output\n", 400)
	if err := os.WriteFile(path, []byte(prefix+"first tail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := worktreeDiffSHA256(context.Background(), root, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(prefix+"other tail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := worktreeDiffSHA256(context.Background(), root, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("tail change invisible to fingerprint")
	}
	testGit(t, root, "add", ".")
	staged, err := worktreeDiffSHA256(context.Background(), root, base, true)
	if err != nil || staged != second {
		t.Fatalf("staged digest differs: %s %s %v", staged, second, err)
	}
}
