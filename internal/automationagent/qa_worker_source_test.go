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
	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
)

func TestQAWorkerPreservesReviewedManifestAndRejectsNewSourceBeforeInference(t *testing.T) {
	for _, scenario := range []struct{ changed, published bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		changed, published := scenario.changed, scenario.published
		t.Run(fmt.Sprintf("changed=%v/published=%v", changed, published), func(t *testing.T) {
			root := setupImplementationRepository(t)
			worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, "a4a4b837-2e18-43af-9f58-6d59629db2bb")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("reviewed source\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var publishedSHA string
			if published {
				for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "synthetic published revision"}, {"remote", "add", "origin", "https://github.com/acme/synthetic-qa.git"}} {
					result, err := runLocal(context.Background(), worktree, commandTimeout, "", "git", args...)
					if err != nil || result.ExitCode != 0 {
						t.Fatal("published fixture", err, result)
					}
				}
				head, err := runLocal(context.Background(), worktree, commandTimeout, "", "git", "rev-parse", "HEAD")
				if err != nil || head.ExitCode != 0 {
					t.Fatal(err, head)
				}
				publishedSHA = strings.TrimSpace(head.Output)
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
			deliveryFields := map[string]any{
				"work_item":     map[string]string{"preview_url": preview.URL},
				"change_sets":   []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "review_type": "local_worktree", "ci_status": "passed", "metadata": metadata}},
				"approved_plan": map[string]any{"qa_execution_matrix": []any{map[string]any{"repository_ref": "workspace://repo", "run_validation": true, "run_qa": false, "run_stagehand": false, "collect_evidence": false}}},
			}
			var matrixDigest string
			if published {
				revisions := []releasegate.Revision{{Repository: "acme/synthetic-qa", Branch: "main", SHA: publishedSHA}}
				matrixDigest, err = releasegate.RevisionMatrixDigest(revisions)
				if err != nil {
					t.Fatal(err)
				}
				deliveryFields["gatekeeper"] = map[string]any{"revisions": revisions}
				deliveryFields["change_sets"] = []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": publishedSHA, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": "acme/synthetic-qa", "target_branch": "main"}}}
			}
			delivery, err := json.Marshal(deliveryFields)
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
			if published {
				message.Payload.TaskID = "a4a4b837-2e18-43af-9f58-6d59629db2bb"
			}
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
			key := "itbem-ai-outputs-local/automation/" + message.Payload.TaskID + "/runs/" + last.RunID + "/result.json"
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
			if (!published && (binding["review_source_sha256"] != metadata["review_source_sha256"] || binding["review_diff_sha256"] != metadata["review_diff_sha256"])) || run["branch"] != branch || len(commands) != 1 || commands[0].(map[string]any)["passed"] != true {
				t.Fatalf("private worker result lost exact reviewed execution: %#v", run)
			}
			if published {
				raw, err := json.Marshal(last.Execution)
				if err != nil {
					t.Fatal(err)
				}
				observation, err := qaevidence.Decode(raw)
				if err != nil || observation.MatrixDigest != matrixDigest || observation.TaskID != message.Payload.TaskID || len(observation.Repositories) != 1 || len(observation.Repositories[0].Commands) != 1 || !observation.Repositories[0].Commands[0].Passed {
					t.Fatalf("invalid callback observation: %#v %v", observation, err)
				}
				saved, _ := json.Marshal(result["execution"])
				if string(saved) != string(raw) {
					t.Fatal("private result lost callback observation")
				}
			}
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			replayed := callback.updates[len(callback.updates)-1]
			if published {
				original, _ := json.Marshal(last.Execution)
				recovered, _ := json.Marshal(replayed.Execution)
				if string(original) != string(recovered) {
					t.Fatal("recovery changed canonical observation")
				}
			}
			if provider.calls != 1 || replayed.Status != "completed" || replayed.OutputRef != last.OutputRef || replayed.RecoveryRunID != last.RunID || replayed.CallID != last.CallID || replayed.ReceiptID != last.ReceiptID {
				t.Fatalf("redelivery repeated inference or lost evidence identity: calls=%d replay=%#v", provider.calls, replayed)
			}
		})
	}
}
