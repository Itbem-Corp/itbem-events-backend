package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"events-stocks/internal/agentwork"
	"events-stocks/internal/releasegate"
)

func TestQAWorkerAcquiresIndependentSourceRunsTestsAndRecoversWithoutNewFetch(t *testing.T) {
	commit, pack := qaSourcePackFixture(t, func(root string) error {
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module synthetic-source\n\ngo 1.25\n"), 0600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "source_test.go"), []byte(`package source
import("os";"testing")
func TestFrozenUnchangedSource(t *testing.T){body,err:=os.ReadFile("unchanged.txt");if err!=nil||string(body)!="stable source\n"{t.Fatal("source changed")}}
`), 0600)
	})
	const task = "11111111-1111-4111-8111-111111111111"
	branch := "itbem-agent/" + task
	qaRoot := t.TempDir()
	registry, err := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: qaRoot, RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main", ValidationCommands: [][]string{{"go", "test", "./..."}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(registry))
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer preview.Close()
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: task, Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: commit}}, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	matrix, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := json.Marshal(map[string]any{"work_item": map[string]string{"preview_url": preview.URL}, "gatekeeper": candidate, "change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": commit, "review_type": "pull_request", "ci_status": "passed", "metadata": map[string]string{"remote_repository": "example/service", "target_branch": "main"}}}, "approved_plan": map[string]any{"qa_execution_matrix": []any{map[string]any{"repository_ref": "workspace://repo", "run_validation": true, "run_qa": false, "run_stagehand": false, "collect_evidence": false}}}})
	if err != nil {
		t.Fatal(err)
	}
	identity, instance := newTestMachineIdentity(t)
	var acquisitions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid source request")
			w.WriteHeader(400)
			return
		}
		acquisitions.Add(1)
		raw, _ := json.Marshal(QASourceMetadata{SchemaVersion: 1, TaskID: task, RunID: request["run_id"], MatrixDigest: matrix, Reference: "workspace://repo", Repository: "example/service", Branch: branch, CommitSHA: commit, PackSHA256: fmt.Sprintf("%x", sha256.Sum256(pack))})
		w.Header().Set("X-ITBEM-QA-Source", base64.RawURLEncoding.EncodeToString(raw))
		w.Header().Set("Content-Type", "application/x-git-packed-objects")
		_, _ = w.Write(pack)
	}))
	defer server.Close()
	signed := newTestCallbackWithIdentity(t, server, identity, instance)
	gateway, err := NewHTTPGateway(server.URL, "synthetic-token", agentwork.RoleQA, agentwork.LaneQA, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(TaskInput{Prompt: "Summarize observed synthetic QA", Delivery: delivery})
	if err != nil {
		t.Fatal(err)
	}
	store, callback := &fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}, &fakeCallback{operation: "delivery.qa"}
	provider := &countingProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"summary":"synthetic QA","verdict":"passed","checks":[],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`, Usage: map[string]any{}, CallID: "b00cc804-1c27-4a30-9543-c0275f4d34a3", ReceiptID: "25a26d3e-c23d-435d-a2b3-eaa5a47b13fc"}}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", Role: agentwork.RoleQA, Lane: agentwork.LaneQA}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	worker.qaSourceAcquirer = func(ctx context.Context, taskID, runID, reference string, delivery json.RawMessage) (string, error) {
		return signed.AcquireQASource(ctx, gateway, taskID, runID, reference, delivery, os.Getenv)
	}
	message := validMessage()
	message.Payload.TaskID = task
	message.Payload.Operation = "delivery.qa"
	ctx := gateway.BindMessageContext(context.Background(), QueueMessage{ReceiptHandle: "sealed-synthetic-lease"})
	if err := worker.Process(ctx, message); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if last.Status != "completed" || provider.calls != 1 || acquisitions.Load() != 1 {
		t.Fatalf("QA source workflow failed: %s provider=%d source=%d", last.Status, provider.calls, acquisitions.Load())
	}
	var result struct {
		Artifacts struct {
			QA struct {
				Runs []struct {
					Commands []struct {
						Passed bool `json:"passed"`
					} `json:"commands"`
					Binding map[string]string `json:"review_binding"`
				} `json:"repository_runs"`
			} `json:"qa_execution"`
		} `json:"artifacts"`
	}
	key := "itbem-ai-outputs-local/automation/" + task + "/runs/" + last.RunID + "/result.json"
	if err := json.Unmarshal(store.writes[key], &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts.QA.Runs) != 1 || len(result.Artifacts.QA.Runs[0].Commands) != 1 || !result.Artifacts.QA.Runs[0].Commands[0].Passed || result.Artifacts.QA.Runs[0].Binding["commit_sha"] != commit {
		t.Fatal("worker lost exact tested source evidence")
	}
	if err := worker.Process(ctx, message); err != nil {
		t.Fatal(err)
	}
	recovered := callback.updates[len(callback.updates)-1]
	if acquisitions.Load() != 1 || provider.calls != 1 || recovered.OutputRef != last.OutputRef || recovered.RecoveryRunID != last.RunID {
		t.Fatal("recovery acquired source or repeated synthetic inference")
	}
}
