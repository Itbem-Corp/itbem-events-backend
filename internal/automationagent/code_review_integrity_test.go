package automationagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCodeReviewWorkerRejectsAmbiguousPrimaryAndRepair(t *testing.T) {
	valid := `{"summary":"Synthetic review control.","verdict":"approve","review_scope":["handler"],"findings":[],"test_plan":["Run unit tests."],"coverage_gaps":[]}`
	duplicate := strings.Replace(valid, `"verdict":"approve"`, `"verdict":"blocked","verdict":"approve"`, 1)
	for _, tc := range []struct {
		name, status string
		responses    []string
	}{
		{"valid", "completed", []string{valid}},
		{"primary-repaired", "completed", []string{duplicate, valid}},
		{"ambiguous-repair", "failed", []string{duplicate, duplicate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(TaskInput{Prompt: "Review frozen source", Delivery: validCodeReviewInput()})
			if err != nil {
				t.Fatal(err)
			}
			store := &fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}
			callback := &fakeCallback{operation: "code.review"}
			provider := &sequenceProvider{responses: tc.responses}
			worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, provider)
			if err != nil {
				t.Fatal(err)
			}
			message := validMessage()
			message.Payload.Operation = "code.review"
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			last := callback.updates[len(callback.updates)-1]
			if last.Status != tc.status || provider.calls != len(tc.responses) {
				t.Fatalf("unexpected result: %+v calls=%d", last, provider.calls)
			}
			var result map[string]any
			key := "itbem-ai-outputs-local/automation/task/runs/" + last.RunID + "/result.json"
			if err := json.Unmarshal(store.writes[key], &result); err != nil {
				t.Fatal(err)
			}
			if tc.name != "valid" && !strings.Contains(result["content"].(string), "blocked") {
				t.Fatal("ambiguous original response lost")
			}
			if tc.status == "failed" {
				structured, _ := result["structured_result"].(map[string]any)
				if len(structured) != 0 || result["validation_error"] == nil {
					t.Fatal("ambiguous repair became a review")
				}
			}
			if err := worker.Process(context.Background(), message); err != nil {
				t.Fatal(err)
			}
			replayed := callback.updates[len(callback.updates)-1]
			if provider.calls != len(tc.responses) || replayed.Status != tc.status || replayed.OutputRef != last.OutputRef {
				t.Fatal("recovery repeated inference or changed result")
			}
		})
	}
}

func TestCodeReviewRejectsAmbiguousJSONBeforeNormalization(t *testing.T) {
	valid := `{"summary":"Synthetic review control.","verdict":"approve","review_scope":["handler"],"findings":[],"test_plan":["Run unit tests."],"coverage_gaps":[]}`
	wrap := func(raw string) string {
		data, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, content := range []string{valid, "```json\n" + valid + "\n```", wrap(valid), strings.TrimSuffix(valid, "]}") + "}"} {
		if _, err := ParseCodeReview(content); err != nil {
			t.Fatalf("supported response rejected: %v", err)
		}
	}
	for _, raw := range []string{
		strings.Replace(valid, `"verdict":"approve"`, `"verdict":"blocked","verdict":"approve"`, 1),
		strings.Replace(valid, `"verdict":"approve"`, `"VERDICT":"blocked","verdict":"approve"`, 1),
		strings.Replace(valid, `"verdict":"approve"`, `"\u0076erdict":"blocked","verdict":"approve"`, 1),
		strings.Replace(valid, `"findings":[]`, `"findings":[],"metadata":{"line_start":1,"LINE_START":2}`, 1),
	} {
		for _, content := range []string{raw, "```json\n" + raw + "\n```", wrap(raw), strings.TrimSuffix(raw, "]}") + "}"} {
			if _, err := ParseCodeReview(content); err == nil {
				t.Fatalf("ambiguous review accepted: %s", content)
			}
		}
	}
	for _, content := range []string{strings.Repeat("x", maxCodeReviewResponseBytes+1), valid + string([]byte{255})} {
		if _, err := ParseCodeReview(content); err == nil {
			t.Fatal("invalid bounded input accepted")
		}
	}
}
