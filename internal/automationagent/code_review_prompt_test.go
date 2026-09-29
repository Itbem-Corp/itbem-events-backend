package automationagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSegmentedCodeReviewPromptReplacesLegacyPatchWithAnnotatedBoundary(t *testing.T) {
	patch := "diff --git a/internal/service.go b/internal/service.go\n--- a/internal/service.go\n+++ b/internal/service.go\n@@ -1 +1 @@\n-old\n+new\n"
	boundary, err := NewCodeReviewInput("github://example/service", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(boundary)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := buildTaskMessagesWithReviewCoverage("code.review", TaskInput{Prompt: "Review the exact segment.", Delivery: raw}, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	combined := ""
	for _, message := range messages {
		combined += message.Content
	}
	if strings.Count(combined, codeReviewBoundaryMarker) != 1 {
		t.Fatalf("segmented prompt must contain one immutable boundary, got %d", strings.Count(combined, codeReviewBoundaryMarker))
	}
	if strings.Contains(combined, "\n\nFrozen patch:\n") {
		t.Fatal("segmented prompt retained the duplicate legacy patch")
	}
	for _, required := range []string{"Frozen patch annotated for evidence selection", "+ [HEAD L1] new", "Location contract:", "copied exactly from changed_line_ranges"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("segmented prompt lost %q", required)
		}
	}
}
