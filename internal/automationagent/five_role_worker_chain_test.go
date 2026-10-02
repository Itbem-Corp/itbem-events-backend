package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"events-stocks/internal/agentwork"
	"github.com/gofrs/uuid"
)

// This cost-free fixture executes business operations through real workers.
// Callback/storage/provider fixtures do not prove gateway or database authority.
func TestFiveRoleWorkersCarryReviewedSourceAndRefuseUngrantableRelease(t *testing.T) {
	root := setupImplementationRepository(t)
	for name, body := range map[string]string{
		"go.mod":      "module synthetic-worker-fixture\n\ngo 1.25.0\n",
		"sum.go":      "package fixture\nfunc Sum(a, b int) int { return a - b }\n",
		"sum_test.go": "package fixture\nimport \"testing\"\nfunc TestSyntheticSum(t *testing.T) { if Sum(19, 23) != 42 { t.Fatal(\"incorrect sum\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "go.mod", "sum.go", "sum_test.go"}, {"commit", "-m", "synthetic failing sum fixture"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", "git", args...)
		if err != nil || result.ExitCode != 0 {
			t.Fatal("fixture commit failed", err)
		}
	}
	negative, err := runLocal(context.Background(), root, commandTimeout, "", "go", "test", "-json", "-count=1", "./...")
	if err != nil || negative.ExitCode != 1 || !strings.Contains(negative.Output, "TestSyntheticSum") {
		t.Fatalf("unit fixture must detect the initial defect: %#v / %v", negative, err)
	}
	remote, err := runLocal(context.Background(), root, commandTimeout, "", "git", "remote", "add", "origin", "https://github.com/acme/synthetic-worker-fixture.git")
	if err != nil || remote.ExitCode != 0 {
		t.Fatal("fixture origin setup failed", err)
	}
	base, err := runLocal(context.Background(), root, commandTimeout, "", "git", "rev-parse", "HEAD")
	if err != nil || base.ExitCode != 0 {
		t.Fatal("fixture base unavailable", err)
	}
	registry, err := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root, Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityCreateWorktree, WorkspaceCapabilityApplyPatch}, ValidationCommands: [][]string{{"go", "test", "-json", "-count=1", "./..."}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(registry))
	marshal := func(value any) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	sources := []any{map[string]any{"kind": "repository", "reference": "workspace://repo", "revision": strings.TrimSpace(base.Output)}}
	run := func(operation, prompt string, delivery any, response string, wantStatus string) (map[string]any, TaskUpdate, string) {
		t.Helper()
		assignment, known := agentwork.AssignmentForOperation(operation)
		if !known {
			t.Fatal("fixture operation unknown")
		}
		taskID := uuid.Must(uuid.NewV4()).String()
		input := marshal(TaskInput{Prompt: prompt, Delivery: marshal(delivery)})
		store := &artifactFakeStore{fakeStore: fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}}
		callback := &fakeCallback{operation: operation}
		provider := &countingProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: response, Usage: map[string]any{}, CallID: uuid.Must(uuid.NewV4()).String(), ReceiptID: uuid.Must(uuid.NewV4()).String()}}
		worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", Role: assignment.Role, Lane: assignment.Lane}, store, callback, provider)
		if err != nil {
			t.Fatal(err)
		}
		message := validMessage()
		message.Payload.TaskID, message.Payload.Operation = taskID, operation
		message.Payload.InputRef = "s3://itbem-ai-inputs-local/automation/inputs/" + taskID + "/input.json"
		if err := worker.Process(context.Background(), message); err != nil {
			t.Fatal(err)
		}
		if len(callback.updates) == 0 {
			t.Fatal("missing worker callbacks")
		}
		last := callback.updates[len(callback.updates)-1]
		if last.Status != wantStatus {
			t.Fatalf("%s did not reach %s: %#v", operation, wantStatus, last)
		}
		wantCalls := 1
		if operation == "delivery.publish" {
			wantCalls = 0
		}
		if provider.calls != wantCalls {
			t.Fatalf("%s provider calls=%d want=%d", operation, provider.calls, wantCalls)
		}
		if wantStatus != "completed" {
			return nil, last, taskID
		}
		var result map[string]any
		key := "itbem-ai-outputs-local/automation/" + taskID + "/runs/" + last.RunID + "/result.json"
		if err := json.Unmarshal(store.writes[key], &result); err != nil {
			t.Fatal(err)
		}
		if result["operation"] != operation || result["task_id"] != taskID {
			t.Fatal("result lost task identity")
		}
		return result, last, taskID
	}
	proposal, _, _ := run("ai.chat", "Describe the bounded synthetic README change", map[string]any{"context_sources": sources}, "Change README from base to reviewed; retain the exact base revision.", "completed")
	patch := marshal(map[string]string{"summary": "bounded synthetic change", "patch": "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-base\n+reviewed\ndiff --git a/sum.go b/sum.go\n--- a/sum.go\n+++ b/sum.go\n@@ -1,2 +1,2 @@\n package fixture\n-func Sum(a, b int) int { return a - b }\n+func Sum(a, b int) int { return a + b }\n"})
	_, implementation, implementationTask := run("delivery.implementation", fmt.Sprint(proposal["content"]), map[string]any{"context_sources": sources}, string(patch), "completed")
	handoff := implementation.Execution
	if handoff["base_sha"] != strings.TrimSpace(base.Output) || !sha256DigestPattern.MatchString(fmt.Sprint(handoff["review_source_sha256"])) {
		t.Fatalf("engineer lost reviewed revision: %#v", handoff)
	}
	worktree := filepath.Join(root, ".itbem-agent-worktrees", implementationTask)
	before, err := sandboxWorktreeDigest(worktree)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = run("delivery.assessment", "Assess only the frozen implementation handoff", map[string]any{"context_sources": sources, "implementation_execution": handoff}, `{"summary":"Synthetic frozen implementation assessed","verdict":"assessed","evidence":["Frozen implementation handoff"],"risks":[],"limitations":["Synthetic fixture"],"recommended_next_steps":["Run registered QA"]}`, "completed")
	after, err := sandboxWorktreeDigest(worktree)
	if err != nil || before != after {
		t.Fatal("read-only reviewer modified implementation source", err)
	}
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer preview.Close()
	delivery := map[string]any{"context_sources": sources, "work_item": map[string]string{"preview_url": preview.URL}, "change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": handoff["branch"], "review_type": "local_worktree", "ci_status": "passed", "metadata": handoff}}, "approved_plan": map[string]any{"qa_execution_matrix": []any{map[string]any{"repository_ref": "workspace://repo", "run_validation": true, "run_qa": false, "run_stagehand": false, "collect_evidence": false}}}}
	qa, _, _ := run("delivery.qa", "Summarize only observed validation", delivery, `{"summary":"Synthetic QA evidence","verdict":"passed","checks":[],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`, "completed")
	runs := qa["artifacts"].(map[string]any)["qa_execution"].(map[string]any)["repository_runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["review_binding"].(map[string]any)["review_source_sha256"] != handoff["review_source_sha256"] {
		t.Fatal("QA lost the engineer source authority")
	}
	commands := runs[0].(map[string]any)["commands"].([]any)
	if len(commands) != 1 || commands[0].(map[string]any)["passed"] != true || !strings.Contains(fmt.Sprint(commands[0].(map[string]any)["output"]), "TestSyntheticSum") {
		t.Fatalf("QA did not execute the functional unit test: %#v", commands)
	}
	delivery["qa_execution"] = qa["artifacts"].(map[string]any)["qa_execution"]
	_, release, _ := run("delivery.publish", "Publish only if independently authorized", delivery, "must never infer", "failed")
	if !strings.Contains(release.ErrorMessage, "human authorization") {
		t.Fatalf("release did not refuse the missing independent grant: %#v", release)
	}
	if body, err := os.ReadFile(filepath.Join(root, "README.md")); err != nil || string(body) != "base\n" {
		t.Fatal("workflow changed the base checkout", err)
	}
	if body, err := os.ReadFile(filepath.Join(worktree, "README.md")); err != nil || string(body) != "reviewed\n" {
		t.Fatal("reviewed source was lost", err)
	}
}
