package delivery

import (
	"events-stocks/models"
	"testing"
)

func TestConversationContinuationPreservesHumanGates(t *testing.T) {
	for _, state := range []string{"plan_review", "code_review", "qa_review", "preview_pending", "released", "cancelled", "blocked"} {
		if _, err := messageContinuationPhase(models.DeliveryWorkItem{State: state}, 0, 0); err == nil {
			t.Fatalf("conversation bypassed %s", state)
		}
	}
	for state, phase := range map[string]string{"planning": "plan", "implementation": "implementation", "qa_running": "qa", "release_review": "summary"} {
		item := models.DeliveryWorkItem{State: state, AutomationEpoch: 2}
		got, err := messageContinuationPhase(item, 2, 0)
		if err != nil || got != phase {
			t.Fatalf("%s: %s %v", state, got, err)
		}
		if _, err := messageContinuationPhase(item, 1, 0); err == nil {
			t.Fatal("accepted stale decision")
		}
		if _, err := messageContinuationPhase(item, 2, 1); err == nil {
			t.Fatal("forked active work")
		}
		item.AgentProgress = "queued"
		if _, err := messageContinuationPhase(item, 2, 0); err == nil {
			t.Fatal("duplicated pending continuation")
		}
	}
}

func TestConversationCannotRepeatUncertainInference(t *testing.T) {
	for _, message := range []string{"Provider outcome uncertain", "Provider request did not return a durable answer", "private recovery accounting is invalid", "provider response storage unavailable"} {
		if !ambiguousAgentOutcome(message) {
			t.Fatal(message)
		}
	}
	if ambiguousAgentOutcome("Agent call budget exhausted") {
		t.Fatal("ordinary exhaustion incorrectly treated as unknown outcome")
	}
}
