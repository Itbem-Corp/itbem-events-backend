package modelevaluation

import (
	"testing"

	"events-stocks/internal/automationagent"
)

func TestImplementationPilotReservesExactWorkerEnvelopeWithoutAdmission(t *testing.T) {
	if SupportedCorpus(ImplementationPilotVersion) {
		t.Fatal("prepared pilot must not silently enable central admission")
	}
	base, err := automationagent.SyntheticChatMessages("overhead")
	if err != nil {
		t.Fatal(err)
	}
	overhead := 0
	for _, message := range base {
		overhead += len(message.Role) + len(message.Content) + 64
	}
	plan, err := PrepareImplementationPilot(overhead, testPricing)
	if err != nil || len(plan.Calls) != 3 || plan.ReservationMicros <= 0 || plan.ReservationMicros > MaxBudgetMicros {
		t.Fatalf("pilot reservation invalid: %#v / %v", plan, err)
	}
	seen := map[Candidate]bool{}
	for _, call := range plan.Calls {
		if call.CaseID != "pagination-v1" || seen[call.Candidate] || call.PromptSHA256 != Digest([]byte(call.Prompt)) {
			t.Fatalf("invalid pilot call binding: %#v", call)
		}
		seen[call.Candidate] = true
		messages, err := automationagent.SyntheticChatMessages(call.Prompt)
		if err != nil {
			t.Fatal(err)
		}
		actual := 0
		for _, message := range messages {
			actual += len(message.Role) + len(message.Content) + 64
		}
		if actual > len(call.Prompt)+overhead {
			t.Fatal("worker envelope exceeds reserved input bound")
		}
	}
	if _, err := PrepareImplementationPilot(overhead, "{}"); err == nil {
		t.Fatal("unpriced pilot accepted")
	}
	if _, err := Compile([]Case{{ID: "pagination-v1", Prompt: "synthetic"}}, "instruction", overhead, testPricing); err == nil {
		t.Fatal("existing twenty-case contract was weakened")
	}
	t.Logf("synthetic price catalog reserves %d micro-USD for three calls", plan.ReservationMicros)
}
