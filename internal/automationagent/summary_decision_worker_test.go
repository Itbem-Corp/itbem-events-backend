package automationagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryWorkerGroundsDecisionClaimsAndPreservesRecovery(t *testing.T) {
	delivery := json.RawMessage(`{"work_item":{"id":"task"},"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test"}],"gates":[{"id":"gate-plan","kind":"plan","decision":"approved"},{"id":"gate-qa","kind":"qa_review","decision":"request_changes"}]}`)
	base := `{"executive":{"what_changed":"Synthetic flow","why":"Ground decisions","how_to_test":"Read evidence","risks":["Human gate"]},"technical":{"decisions":["Plan not approved; QA approved"],"evidence":["1e5c5eb5-38cb-46af-9e50-a196ad7fc333 — Synthetic authorization test"]`
	claims := `,"decision_claims":{"schema_version":1,"gates":[{"index":0,"gate_id":"gate-plan","kind":"plan","decision":"approved"},{"index":1,"gate_id":"gate-qa","kind":"qa_review","decision":"request_changes"}]}`
	for _, tc := range []struct{ name, claims, status string }{
		{"correct", claims, "completed"},
		{"missing", "", "failed"},
		{"swapped", strings.ReplaceAll(strings.ReplaceAll(claims, "approved", "swap"), "request_changes", "approved"), "failed"},
		{"duplicate", strings.Replace(claims, `"index":0`, `"index":1,"index":0`, 1), "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(TaskInput{Prompt: "Prepare a grounded draft", Delivery: delivery})
			if err != nil {
				t.Fatal(err)
			}
			store := &fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}
			callback := &fakeCallback{operation: "delivery.summary"}
			provider := &countingProvider{completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: base + tc.claims + `}}`, Usage: map[string]any{"total_tokens": 9}, CallID: "b00cc804-1c27-4a30-9543-c0275f4d34a3", ReceiptID: "25a26d3e-c23d-435d-a2b3-eaa5a47b13fc"}}
			worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, provider)
			if err != nil {
				t.Fatal(err)
			}
			message := validMessage()
			message.Payload.Operation = "delivery.summary"
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			last := callback.updates[len(callback.updates)-1]
			if last.Status != tc.status || last.OutputRef == "" || provider.calls != 1 {
				t.Fatalf("unexpected terminal state: %+v calls=%d", last, provider.calls)
			}
			key := "itbem-ai-outputs-local/automation/task/runs/" + last.RunID + "/result.json"
			var result map[string]any
			if err := json.Unmarshal(store.writes[key], &result); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result["content"].(string), "Plan not approved; QA approved") || result["usage"].(map[string]any)["total_tokens"] != float64(9) {
				t.Fatal("original narrative or usage lost")
			}
			structured := result["structured_result"].(map[string]any)
			if tc.status == "completed" {
				decisions := structured["technical"].(map[string]any)["decisions"].([]any)
				if len(decisions) != 2 || !strings.Contains(decisions[0].(string), "plan: approved") || !strings.Contains(decisions[1].(string), "qa_review: request_changes") {
					t.Fatal("structured gate pairs corrupted")
				}
			} else if len(structured) != 0 || result["validation_error"] == nil {
				t.Fatal("invalid claims became a report")
			}
			requestKey := "itbem-ai-outputs-local/automation/task/runs/" + last.RunID + "/request.json"
			if !strings.Contains(string(store.writes[requestKey]), "technical.decision_claims") {
				t.Fatal("claims prompt missing")
			}
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			replayed := callback.updates[len(callback.updates)-1]
			if provider.calls != 1 || replayed.Status != tc.status || replayed.OutputRef != last.OutputRef || replayed.RecoveryRunID != last.RunID {
				t.Fatalf("recovery repeated inference or changed verdict: %+v", replayed)
			}
		})
	}
}
