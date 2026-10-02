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
)

func TestQAWorkerPreservesReviewedManifestAndRejectsNewSourceBeforeInference(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			root := setupImplementationRepository(t)
			worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, "a4a4b837-2e18-43af-9f58-6d59629db2bb")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("reviewed source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			metadata := reviewedQAMetadata(t, root, worktree, "HEAD")
			digest, err := sandboxWorktreeDigest(worktree)
			if err != nil {
				t.Fatal(err)
			}
			metadata["review_source_sha256"] = fmt.Sprintf("%x", digest)
			if changed {
				if err := os.WriteFile(filepath.Join(worktree, "unreviewed_test.go"), []byte("package fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer preview.Close()
			registry, err := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root, ValidationCommands: [][]string{{"go", "version"}}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(registry))
			delivery, err := json.Marshal(map[string]any{
				"work_item":     map[string]string{"preview_url": preview.URL},
				"change_sets":   []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "review_type": "local_worktree", "ci_status": "passed", "metadata": metadata}},
				"approved_plan": map[string]any{"qa_execution_matrix": []any{map[string]any{"repository_ref": "workspace://repo", "run_validation": true, "run_qa": false, "run_stagehand": false, "collect_evidence": false}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(TaskInput{Prompt: "Summarize only the observed synthetic QA evidence", Delivery: delivery})
			if err != nil {
				t.Fatal(err)
			}
			store, callback := &fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}, &fakeCallback{operation: "delivery.qa"}
			provider := &countingProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"summary":"synthetic observed QA","verdict":"passed","checks":[],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`, Usage: map[string]any{}, CallID: "b00cc804-1c27-4a30-9543-c0275f4d34a3", ReceiptID: "25a26d3e-c23d-435d-a2b3-eaa5a47b13fc"}}
			worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", Role: agentwork.RoleQA, Lane: agentwork.LaneQA}, store, callback, provider)
			if err != nil {
				t.Fatal(err)
			}
			message := validMessage()
			message.Payload.Operation = "delivery.qa"
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			if len(callback.updates) == 0 {
				t.Fatal("worker produced no terminal evidence")
			}
			last := callback.updates[len(callback.updates)-1]
			if changed {
				if last.Status != "failed" || provider.calls != 0 {
					t.Fatalf("unreviewed source reached inference: calls=%d terminal=%#v", provider.calls, last)
				}
				for key := range store.writes {
					if strings.HasSuffix(key, "/provider-intent.json") || strings.HasSuffix(key, "/request.json") {
						t.Fatal("rejected QA reserved a provider call")
					}
				}
				return
			}
			if last.Status != "completed" || provider.calls != 1 {
				t.Fatalf("reviewed QA did not complete: calls=%d terminal=%#v", provider.calls, last)
			}
			var result map[string]any
			key := "itbem-ai-outputs-local/automation/task/runs/" + last.RunID + "/result.json"
			if err := json.Unmarshal(store.writes[key], &result); err != nil {
				t.Fatal(err)
			}
			artifacts := result["artifacts"].(map[string]any)
			// The result must retain deterministic execution evidence independently of the model summary.
			execution := artifacts["qa_execution"].(map[string]any)
			runs := execution["repository_runs"].([]any)
			if len(runs) != 1 {
				t.Fatalf("repository evidence missing: %#v", execution)
			}
			run := runs[0].(map[string]any)
			binding := run["review_binding"].(map[string]any)
			commands := run["commands"].([]any)
			if binding["review_source_sha256"] != metadata["review_source_sha256"] || binding["review_diff_sha256"] != metadata["review_diff_sha256"] || run["branch"] != branch || len(commands) != 1 || commands[0].(map[string]any)["passed"] != true {
				t.Fatalf("private worker result lost exact reviewed execution: %#v", run)
			}
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			replayed := callback.updates[len(callback.updates)-1]
			if provider.calls != 1 || replayed.Status != "completed" || replayed.OutputRef != last.OutputRef || replayed.RecoveryRunID != last.RunID || replayed.CallID != last.CallID || replayed.ReceiptID != last.ReceiptID {
				t.Fatalf("redelivery repeated inference or lost evidence identity: calls=%d replay=%#v", provider.calls, replayed)
			}
		})
	}
}
