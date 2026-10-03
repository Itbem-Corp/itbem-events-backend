package modelevaluation

import (
	"testing"

	"events-stocks/internal/automationagent"
)

func TestImplementationPilotReservesExactWorkerEnvelopeWithVersionedAdmission(t *testing.T) {
	for version, expected := range map[string]int{CorpusVersion: 60, CacheCorpusVersion: 60, ImplementationPilotVersion: 3} {
		count, err := ExpectedCalls(version)
		if err != nil || count != expected {
			t.Fatalf("incorrect corpus cardinality: %s / %d / %v", version, count, err)
		}
	}
	if _, err := ExpectedCalls("unknown"); err == nil {
		t.Fatal("unknown corpus cardinality accepted")
	}
	if !SupportedCorpus(ImplementationPilotVersion) {
		t.Fatal("published pilot is missing versioned admission")
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
	cases, instruction, _, err := CorpusForVersion(ImplementationPilotVersion)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := CompileForVersion(ImplementationPilotVersion, cases, instruction, overhead, testPricing)
	if err != nil || len(admitted.Calls) != 3 || admitted.ReservationMicros != plan.ReservationMicros {
		t.Fatalf("versioned admission differs from prepared pilot: %v", err)
	}
	if _, err := CompileForVersion(ImplementationPilotVersion, append(cases, cases[0]), instruction, overhead, testPricing); err == nil {
		t.Fatal("pilot accepted more than one case")
	}
	if _, err := CompileForVersion(CorpusVersion, cases, instruction, overhead, testPricing); err == nil {
		t.Fatal("screening admitted a one-case corpus")
	}
	frozenPrompt, corpusHash, err := ImplementationPilotInput()
	if err != nil || len(corpusHash) != 64 {
		t.Fatalf("invalid frozen pilot input: %v", err)
	}
	for _, call := range plan.Calls {
		if call.CaseID != "pagination-v1" || seen[call.Candidate] || call.Prompt != frozenPrompt || call.PromptSHA256 != Digest([]byte(call.Prompt)) {
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
