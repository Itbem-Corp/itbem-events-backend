package automationagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewerPromptRequiresUniqueGroundedLocations(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-return false\n+return true\n"
	boundary, err := NewCodeReviewInput("github://example/test", strings.Repeat("a", 40), strings.Repeat("b", 40), patch)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(boundary)
	messages, err := buildTaskMessages("code.review", TaskInput{Prompt: "Review", Delivery: raw}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	combined := messages[0].Content
	for _, message := range messages[1:] {
		combined += "\n" + message.Content
	}
	if !strings.Contains(combined, "unique source locations") || !strings.Contains(combined, "Consolidate related concerns") || !strings.Contains(combined, "changed_line_ranges") || !strings.Contains(combined, "copied exactly") {
		t.Fatal("review contract must explain duplicate-location validation before inference")
	}
}

func TestProductPromptFitsCompleteContractInOutputBudget(t *testing.T) {
	messages, err := buildTaskMessages("product.ideate", TaskInput{Prompt: "Explore alternatives"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []string{"exactly two distinct directions", "under 3,000 UTF-8", "close the JSON", "Label assumptions"} {
		if !strings.Contains(messages[0].Content, requirement) {
			t.Fatalf("missing output-budget constraint %s", requirement)
		}
	}
}
