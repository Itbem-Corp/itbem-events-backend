package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

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
