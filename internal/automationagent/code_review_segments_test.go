package automationagent

import (
	"fmt"
	"strings"
	"testing"
)

func segmentedReviewFixture(t *testing.T) CodeReviewInput {
	t.Helper()
	patch := "diff --git a/src/a.go b/src/a.go\n--- a/src/a.go\n+++ b/src/a.go\n@@ -1 +1 @@\n-oldA\n+newA\n" +
		"diff --git a/src/b_test.go b/src/b_test.go\n--- a/src/b_test.go\n+++ b/src/b_test.go\n@@ -1 +1 @@\n-oldB\n+newB\n"
	input, err := NewCodeReviewInput("github://acme/service", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	input, err = BindCodeReviewContext(input, []CodeReviewContextExcerpt{
		{File: "src/a.go", Side: "head", Start: 1, End: 1, Content: "newA"},
		{File: "src/b_test.go", Side: "head", Start: 1, End: 1, Content: "newB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestSegmentCodeReviewInputPreservesEveryFrozenFileRangeAndContext(t *testing.T) {
	patch := ""
	context := make([]CodeReviewContextExcerpt, 0, codeReviewSegmentMaxFiles+1)
	for index := 0; index < codeReviewSegmentMaxFiles+1; index++ {
		file := fmt.Sprintf("src/file_%02d.go", index)
		patch += fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old%d\n+new%d\n", file, file, file, file, index, index)
		context = append(context, CodeReviewContextExcerpt{File: file, Side: "head", Start: 1, End: 1, Content: fmt.Sprintf("new%d", index)})
	}
	input, err := NewCodeReviewInput("github://acme/service", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	input, err = BindCodeReviewContext(input, context)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || segments[0].Remote != nil || len(segments[0].ChangedFiles) != codeReviewSegmentMaxFiles || len(segments[1].ChangedFiles) != 1 || len(segments[0].Context) != codeReviewSegmentMaxFiles || len(segments[1].Context) != 1 {
		t.Fatalf("unexpected exact-SHA segments: %#v", segments)
	}
	if segments[0].Patch+segments[1].Patch != input.Patch {
		t.Fatal("segmentation changed the frozen patch")
	}
}

func TestAggregateCodeReviewSegmentsCannotApprovePartialOrInvalidEvidence(t *testing.T) {
	input := segmentedReviewFixture(t)
	segments, err := SegmentCodeReviewInput(input)
	if err != nil {
		t.Fatal(err)
	}
	approve, err := ParseCodeReview(`{"summary":"The exact segment is consistent.","verdict":"approve","review_scope":["implementation and tests"],"findings":[],"test_plan":["Run go test ./..."],"coverage_gaps":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := AggregateCodeReviewSegments(input, segments, []map[string]any{approve})
	if err != nil || aggregate["verdict"] != "approve" {
		t.Fatalf("complete valid segments should aggregate: %#v / %v", aggregate, err)
	}
	if _, err := AggregateCodeReviewSegments(input, segments, nil); err == nil {
		t.Fatal("partial segment results were accepted")
	}
	invalid, _ := ParseCodeReview(`{"summary":"An issue exists.","verdict":"request_changes","review_scope":["implementation"],"findings":[{"id":"outside","severity":"high","category":"correctness","title":"Outside","file":"src/a.go","side":"head","line_start":50,"line_end":50,"evidence":"Outside the diff.","evidence_quote":"newA","recommendation":"Correct the changed line.","confidence":0.9}],"test_plan":["Run tests"],"coverage_gaps":[]}`)
	if _, err := AggregateCodeReviewSegments(input, segments, []map[string]any{invalid}); err == nil {
		t.Fatal("segment evidence outside its exact changed lines was accepted")
	}
}

func TestBoundedCodeReviewSegmentContextIsFairAndStrictlyCapped(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go"}
	excerpts := make([]CodeReviewContextExcerpt, 0, 30)
	for round := 0; round < 10; round++ {
		for _, file := range files {
			excerpts = append(excerpts, CodeReviewContextExcerpt{File: file, Side: "head", Start: round + 1, End: round + 1, Content: strings.Repeat(file, 2048)})
		}
	}
	got := boundedCodeReviewSegmentContext(files, excerpts)
	total, seen := 0, map[string]bool{}
	for _, excerpt := range got {
		total += len(excerpt.Content)
		seen[excerpt.File] = true
	}
	if total > codeReviewSegmentMaxContext || len(got) > codeReviewSegmentMaxExcerpts {
		t.Fatalf("segment context exceeded its cap: bytes=%d excerpts=%d", total, len(got))
	}
	for _, file := range files {
		if !seen[file] {
			t.Fatalf("round-robin context starved %s: %#v", file, got)
		}
	}
}

func TestSegmentCodeReviewInputReviewsHighRiskFilesFirstWithoutLosingCoverage(t *testing.T) {
	patch := ""
	for index := 0; index < codeReviewSegmentMaxFiles; index++ {
		file := fmt.Sprintf("docs/note_%02d.md", index)
		patch += fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old%d\n+new%d\n", file, file, file, file, index, index)
	}
	securityFile := "internal/auth/token_policy.go"
	patch += fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-old\n+new\n", securityFile, securityFile, securityFile, securityFile)
	input, err := NewCodeReviewInput("github://acme/service", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 || !strings.HasPrefix(segments[0].Patch, "diff --git a/"+securityFile+" ") {
		t.Fatalf("risk-ranked review did not prioritize the security boundary: %#v", segments)
	}
	if err := validateCodeReviewSegmentCoverage(input, segments); err != nil {
		t.Fatalf("risk ordering lost exact coverage: %v", err)
	}
}

func TestSegmentCodeReviewInputPreservesPatchWithoutTerminalNewline(t *testing.T) {
	patch := "diff --git a/docs/readme.md b/docs/readme.md\n--- a/docs/readme.md\n+++ b/docs/readme.md\n@@ -1 +1 @@\n-old\n+new\n" +
		"diff --git a/internal/auth.go b/internal/auth.go\n--- a/internal/auth.go\n+++ b/internal/auth.go\n@@ -1 +1 @@\n-old\n+new"
	input, err := NewCodeReviewInput("github://acme/service", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	segments, err := SegmentCodeReviewInput(input)
	if err != nil || len(segments) != 1 || segments[0].Patch != patch {
		t.Fatalf("patch without terminal newline was altered: %#v / %v", segments, err)
	}
}
