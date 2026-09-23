package automationagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanMessagesEndWithFrozenReferenceContract(t *testing.T) {
	input := TaskInput{Prompt: "Plan a synthetic change", Delivery: json.RawMessage(`{"context_sources":[{"kind":"document","reference":"document://brief","revision":"v1"}]}`)}
	messages, err := buildTaskMessages("delivery.plan", input, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(messages[len(messages)-1].Content, `["document://brief"]`) {
		t.Fatal("planner must see the exact frozen identity contract at the response boundary")
	}
	invalid := map[string]any{"context_reviewed": []any{"document://brief v1: reviewed"}}
	if ValidateDeliveryPlanContextCoverage(invalid, input.Delivery) == nil {
		t.Fatal("the prompt improvement must not weaken frozen-source validation")
	}
}
