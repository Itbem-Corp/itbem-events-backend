package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestSingleSegmentReviewReceivesM3CompletionBudget(t *testing.T) {
	calls := []codeReviewProviderCall{{Messages: []Message{{Role: "user", Content: "Review the frozen patch."}}}}
	allocations, err := allocateCodeReviewCompletionTokens(calls, CompletionTokensForOperation("code.review"))
	if err != nil || len(allocations) != 1 || allocations[0] != 32768 {
		t.Fatalf("single review segment allocation = %v / %v, want 32768", allocations, err)
	}
}

func TestWorkerRoutesLargeCodeReviewThroughLeasedSegments(t *testing.T) {
	var patch strings.Builder
	for index := 0; index < codeReviewSegmentMaxFiles+1; index++ {
		file := fmt.Sprintf("internal/review/file_%02d.go", index)
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-oldValue%d\n+newValue%d\n", file, file, file, file, index, index)
	}
	boundary, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch.String())
	if err != nil {
		t.Fatal(err)
	}
	encodedBoundary, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(TaskInput{Prompt: "Review every frozen file.", Delivery: encodedBoundary})
	if err != nil {
		t.Fatal(err)
	}
	review := `{"summary":"The frozen segment is internally consistent.","verdict":"comment","review_scope":["frozen segment"],"findings":[],"test_plan":["Run the exact-SHA repository checks."],"coverage_gaps":[]}`
	provider := &sequenceProvider{responses: []string{review, review}}
	store, callback := &fakeStore{input: input}, &fakeCallback{operation: "code.review"}
	worker, err := NewWorker(
		WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"},
		store,
		callback,
		provider,
	)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "code.review"
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || len(provider.leases) != 2 || provider.inferenceCapabilities != 2 {
		t.Fatalf("large review used %d provider calls, %d leases, and %d capabilities; want two fully authorized segments", provider.calls, len(provider.leases), provider.inferenceCapabilities)
	}
	runID := callback.updates[0].RunID
	for index, lease := range provider.leases {
		if lease.TaskID != message.Payload.TaskID || lease.RunID != runID || lease.Operation != "code.review" || lease.StepID != "" {
			t.Fatalf("segment %d used the wrong inference lease: %#v", index+1, lease)
		}
	}
	thinkingRenewals := 0
	for _, update := range callback.updates {
		if update.Status == "running" && update.ProgressStep == "thinking" {
			thinkingRenewals++
			if update.RunID != runID || update.ProgressCall != 0 {
				t.Fatalf("segment renewal escaped its active run: %#v", update)
			}
		}
	}
	terminal := callback.updates[len(callback.updates)-1]
	if thinkingRenewals != 2 || terminal.Status != "completed" {
		t.Fatalf("review did not renew each segment and complete: %#v", callback.updates)
	}
	if !validReceiptUUID(terminal.CallID) || !validReceiptUUID(terminal.ReceiptID) {
		t.Fatalf("segmented review discarded its gateway receipt: %#v", terminal)
	}
}

func TestSegmentedReviewUsesFullPRTestEvidenceForCoverageNormalization(t *testing.T) {
	var patch strings.Builder
	for index := 0; index < codeReviewSegmentMaxFiles; index++ {
		file := fmt.Sprintf("internal/review/file_%02d.go", index)
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-oldValue%d\n+newValue%d\n", file, file, file, file, index, index)
	}
	testFile := "tests/review.test.ts"
	fmt.Fprintf(&patch, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-oldTest\n+newTest\n", testFile, testFile, testFile, testFile)

	boundary, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch.String())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(boundary)
	if err != nil || len(segments) != 2 {
		t.Fatalf("expected production and test changes in separate segments: %d / %v", len(segments), err)
	}
	segmentNeedsGap := false
	for _, segment := range segments {
		segmentNeedsGap = segmentNeedsGap || reviewNeedsCoverageGap(segment)
	}
	if reviewNeedsCoverageGap(boundary) || !segmentNeedsGap {
		t.Fatalf("fixture must have full-PR test evidence outside the production-only segment")
	}
	encodedBoundary, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(TaskInput{Prompt: "Review every frozen file.", Delivery: encodedBoundary})
	if err != nil {
		t.Fatal(err)
	}
	review := `{"summary":"The frozen changes are internally consistent.","verdict":"approve","review_scope":["frozen segment"],"findings":[],"test_plan":["Run the exact-SHA repository checks."],"coverage_gaps":[]}`
	provider := &sequenceProvider{responses: []string{review, review}}
	store, callback := &fakeStore{input: input}, &fakeCallback{operation: "code.review"}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "code.review"
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	terminal := callback.updates[len(callback.updates)-1]
	if terminal.Status != "completed" {
		t.Fatalf("segmented review did not complete: %#v", terminal)
	}
	resultRaw := store.writes["itbem-ai-outputs-local/automation/task/runs/"+terminal.RunID+"/result.json"]
	var result struct {
		StructuredResult map[string]any `json:"structured_result"`
	}
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		t.Fatal(err)
	}
	gaps, ok := result.StructuredResult["coverage_gaps"].([]any)
	if result.StructuredResult["verdict"] != "approve" || !ok || len(gaps) != 0 {
		t.Fatalf("a test diff in another segment must prevent a false full-PR coverage gap: %#v", result.StructuredResult)
	}
}

func TestCodeReviewRepairPromptRestatesStrictOutputTypes(t *testing.T) {
	messages := codeReviewRepairMessages([]Message{{Role: "user", Content: "Review the frozen diff."}}, `{}`, errors.New("code review coverage_gaps must be a bounded list of strings"), CodeReviewInput{})
	if len(messages) != 2 {
		t.Fatalf("repair must preserve the review context and append focused feedback, got %#v", messages)
	}
	feedback := messages[len(messages)-1].Content
	for _, required := range []string{
		"review_scope, test_plan, and coverage_gaps are arrays of plain JSON strings",
		"findings is an array of finding objects",
		"never null or a scalar string",
		"An approval is invalid whenever findings or coverage_gaps is non-empty",
	} {
		if !strings.Contains(feedback, required) {
			t.Errorf("repair feedback does not restate %q", required)
		}
	}
}

func TestWorkerCanRepairEveryInvalidReviewSegmentOnce(t *testing.T) {
	var patch strings.Builder
	for index := 0; index < 2*codeReviewSegmentMaxFiles+1; index++ {
		file := fmt.Sprintf("internal/review/file_%02d.go", index)
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-oldValue%d\n+newValue%d\n", file, file, file, file, index, index)
	}
	boundary, err := NewCodeReviewInput("github://itbem/example", strings.Repeat("a", 40), strings.Repeat("b", 40), patch.String())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(boundary)
	if err != nil || len(segments) != 3 {
		t.Fatalf("expected three review segments: %d / %v", len(segments), err)
	}
	encodedBoundary, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(TaskInput{Prompt: "Review every frozen file.", Delivery: encodedBoundary})
	if err != nil {
		t.Fatal(err)
	}
	invalid := `{"summary":"Review complete.","verdict":"comment","review_scope":"wrong shape","findings":[],"test_plan":["Run checks."],"coverage_gaps":[]}`
	valid := `{"summary":"The frozen segment is internally consistent.","verdict":"comment","review_scope":["frozen segment"],"findings":[],"test_plan":["Run the exact-SHA repository checks."],"coverage_gaps":[]}`
	provider := &sequenceProvider{responses: []string{invalid, valid, invalid, valid, invalid, valid}}
	callback := &fakeCallback{operation: "code.review"}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{input: input}, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "code.review"
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 6 || len(provider.leases) != 6 || provider.inferenceCapabilities != 6 {
		t.Fatalf("three segments and their bounded repairs used %d calls, %d leases, %d capabilities", provider.calls, len(provider.leases), provider.inferenceCapabilities)
	}
	if terminal := callback.updates[len(callback.updates)-1]; terminal.Status != "completed" {
		t.Fatalf("repaired review did not complete: %#v", terminal)
	}
}

func TestAggregateCodeReviewCompletionsRetainsFinalGatewayReceipt(t *testing.T) {
	firstCall, firstReceipt := stepCallbackUUID(), stepCallbackUUID()
	finalCall, finalReceipt := stepCallbackUUID(), stepCallbackUUID()
	completion, err := aggregateCodeReviewCompletions([]Completion{
		{Provider: ProviderMiniMax, Model: "MiniMax-M3", ResponseID: "response-1", CallID: firstCall, ReceiptID: firstReceipt, Usage: map[string]any{"total_tokens": 3}, Content: "first"},
		{Provider: ProviderMiniMax, Model: "MiniMax-M3", ResponseID: "response-2", CallID: finalCall, ReceiptID: finalReceipt, Usage: map[string]any{"total_tokens": 5}, Content: "final"},
	}, map[string]any{"verdict": "comment"})
	if err != nil {
		t.Fatal(err)
	}
	if completion.CallID != finalCall || completion.ReceiptID != finalReceipt || completion.ResponseID != "response-2" {
		t.Fatalf("aggregate receipt = call %q receipt %q response %q, want final gateway call", completion.CallID, completion.ReceiptID, completion.ResponseID)
	}
}

func TestCodeReviewProgressRoundTripsOnlyItsExactValidatedSegment(t *testing.T) {
	boundary, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(boundary)
	if err != nil || len(segments) != 1 {
		t.Fatalf("expected one bounded segment: %#v / %v", segments, err)
	}
	review, err := ParseCodeReview(`{"summary":"The changed handler is internally consistent.","verdict":"comment","review_scope":["handler"],"findings":[],"test_plan":["Run the handler test suite."],"coverage_gaps":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	taskID, runID := "checkpoint-task", uuid.Must(uuid.NewV4()).String()
	store := &fakeStore{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, &fakeCallback{}, &sequenceProvider{})
	if err != nil {
		t.Fatal(err)
	}
	calls := []codeReviewProviderCall{{Index: 1, Boundary: segments[0], PatchDigest: segments[0].PatchSHA256}}
	progress := codeReviewProgress{
		SchemaVersion: codeReviewProgressSchemaVersion, TaskID: taskID, BaseSHA: boundary.BaseSHA, HeadSHA: boundary.HeadSHA, PatchSHA256: boundary.PatchSHA256,
		RequestRef: "s3://itbem-ai-outputs-local/automation/" + taskID + "/runs/" + runID + "/request.json",
		Segments: []codeReviewProgressSegment{{
			Index: 1, PatchSHA256: segments[0].PatchSHA256,
			Completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"verdict":"comment"}`, Usage: map[string]any{"total_tokens": float64(12)}},
			Review:     review,
		}},
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := worker.storeCodeReviewProgress(context.Background(), progress); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := worker.loadCodeReviewProgress(context.Background(), taskID, calls, boundary)
	if err != nil || !exists || len(loaded.Segments) != 1 || loaded.Segments[0].Completion.Model != "MiniMax-M3" || loaded.Segments[0].Review["verdict"] != "comment" {
		t.Fatalf("stored exact-segment checkpoint did not round trip: %#v / %v / exists=%t", loaded, err, exists)
	}

	loaded.Segments[0].PatchSHA256 = strings.Repeat("f", 64)
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	store.existing = map[string][]byte{"itbem-ai-outputs-local/" + codeReviewProgressKey(taskID): encoded}
	_, _, err = worker.loadCodeReviewProgress(context.Background(), taskID, calls, boundary)
	var invalid *codeReviewProgressInvalidError
	if !errors.As(err, &invalid) || !errors.Is(err, errCodeReviewProgressFrozenPatch) {
		t.Fatalf("checkpoint for another segment must be rejected before inference: %v", err)
	}
}

func TestCodeReviewProgressRejectsRepairFromAnotherRun(t *testing.T) {
	boundary, err := ParseCodeReviewInput(validCodeReviewInput())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(boundary)
	if err != nil || len(segments) != 1 {
		t.Fatalf("expected one bounded segment: %#v / %v", segments, err)
	}
	taskID := "checkpoint-task"
	runID, otherRunID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{}, &fakeCallback{}, &sequenceProvider{})
	if err != nil {
		t.Fatal(err)
	}
	progress := codeReviewProgress{
		SchemaVersion: codeReviewProgressSchemaVersion, TaskID: taskID, BaseSHA: boundary.BaseSHA, HeadSHA: boundary.HeadSHA, PatchSHA256: boundary.PatchSHA256,
		RequestRef: "s3://itbem-ai-outputs-local/automation/" + taskID + "/runs/" + runID + "/request.json",
		Segments: []codeReviewProgressSegment{{
			Index: 1, PatchSHA256: segments[0].PatchSHA256, RepairAttempted: true,
			Completion:    Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"verdict":"comment"}`, Usage: map[string]any{"total_tokens": float64(12)}},
			PendingRepair: &codeReviewPendingRepair{RequestRef: "s3://itbem-ai-outputs-local/" + codeReviewRepairRequestKey(taskID, otherRunID, 1), ValidationError: "invalid response"},
		}},
	}
	calls := []codeReviewProviderCall{{Index: 1, Boundary: segments[0], PatchDigest: segments[0].PatchSHA256}}
	if err := worker.validateCodeReviewProgress(progress, taskID, calls, boundary); err == nil || !errors.Is(err, errCodeReviewProgressInvalidRepairReference) {
		t.Fatalf("repair checkpoint from another run must be rejected: %v", err)
	}
}
