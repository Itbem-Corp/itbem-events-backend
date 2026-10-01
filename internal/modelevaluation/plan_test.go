package modelevaluation

import (
	"events-stocks/internal/automationagent"
	"fmt"
	"strings"
	"testing"
)

const testPricing = `{"version":"synthetic-test","basis":"api_equivalent","models":{"minimax:minimax-m3":{"input_microusd_per_million":600000,"output_microusd_per_million":2400000},"deepseek:deepseek-flash":{"input_microusd_per_million":300000,"output_microusd_per_million":1200000},"openrouter:openai/gpt-6-luna":{"input_microusd_per_million":200000,"output_microusd_per_million":750000}}}`

func corpus() []Case {
	cases := make([]Case, MaxCases)
	for i := range cases {
		cases[i] = Case{ID: fmt.Sprintf("case-%02d", i), Prompt: "Return a synthetic JSON answer."}
	}
	return cases
}

func TestPublishedCorpusFitsFullWorkerEnvelopeBudget(t *testing.T) {
	for _, version := range []string{CorpusVersion, CacheCorpusVersion} {
		t.Run(version, func(t *testing.T) { testPublishedCorpusBudget(t, version) })
	}
}

func testPublishedCorpusBudget(t *testing.T, version string) {
	cases, instruction, hash, err := CorpusForVersion(version)
	if err != nil || len(cases) != MaxCases || len(hash) != 64 {
		t.Fatalf("invalid published corpus: %v", err)
	}
	base, err := automationagent.SyntheticChatMessages("overhead")
	if err != nil {
		t.Fatal(err)
	}
	overhead := 0
	for _, message := range base {
		overhead += len(message.Role) + len(message.Content) + 64
	}
	plan, err := Compile(cases, instruction, overhead, testPricing)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range plan.Calls {
		messages, err := automationagent.SyntheticChatMessages(call.Prompt)
		if err != nil {
			t.Fatal(err)
		}
		actualBytes := 0
		for _, message := range messages {
			actualBytes += len(message.Role) + len(message.Content) + 64
		}
		if actualBytes > len(call.Prompt)+overhead {
			t.Fatal("worker messages exceed admitted input bound")
		}
	}
	t.Logf("published corpus reserves %d micro-USD across %d calls", plan.ReservationMicros, len(plan.Calls))
}

func TestCacheCorpusPairsAreEquivalentWithBalancedOrder(t *testing.T) {
	cases, instruction, _, err := CorpusForVersion(CacheCorpusVersion)
	if err != nil {
		t.Fatal(err)
	}
	original, oldInstruction, _, err := Corpus()
	if err != nil || instruction != oldInstruction {
		t.Fatal("screening instruction changed")
	}
	var sharedPrefix string
	controlPrefixes := map[string]bool{}
	for pair := 0; pair < 10; pair++ {
		for offset := 0; offset < 2; offset++ {
			current := cases[pair*2+offset]
			condition := "control"
			if (pair+offset)%2 == 1 {
				condition = "shared"
			}
			if current.ID != original[pair].ID+"-"+condition || !strings.HasSuffix(current.Prompt, original[pair].Prompt) {
				t.Fatal("pair order or semantic evidence differs")
			}
			prefix := strings.TrimSuffix(current.Prompt, original[pair].Prompt)
			if condition == "shared" {
				if sharedPrefix != "" && sharedPrefix != prefix {
					t.Fatal("shared prefix varies")
				}
				sharedPrefix = prefix
			} else {
				if controlPrefixes[prefix] {
					t.Fatal("control prefix repeated")
				}
				controlPrefixes[prefix] = true
			}
		}
		// Opaque partition is the only semantic difference before case evidence.
		a := strings.SplitN(cases[pair*2].Prompt, "\n", 2)[1]
		b := strings.SplitN(cases[pair*2+1].Prompt, "\n", 2)[1]
		if a != b {
			t.Fatal("experimental pair has different evidence or protocol")
		}
	}
	if _, _, _, err := CorpusForVersion("client-supplied-prompts"); err == nil {
		t.Fatal("unknown corpus accepted")
	}
	// Decode again: callers cannot mutate the embedded source or history.
	cases[0].Prompt = "mutated"
	reloaded, _, _, _ := CorpusForVersion(CacheCorpusVersion)
	if reloaded[0].Prompt == "mutated" {
		t.Fatal("corpus mutation persisted")
	}
}

func TestPlanPreservesPromptAndSingleCandidateRoutes(t *testing.T) {
	plan, err := Compile(corpus(), "Resolve supplied evidence only.", 4096, testPricing)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Calls) != MaxCalls || plan.ReservationMicros <= 0 || plan.ReservationMicros > MaxBudgetMicros {
		t.Fatalf("invalid admission: %+v", plan)
	}
	for i := 0; i < len(plan.Calls); i += 3 {
		for j := 0; j < 3; j++ {
			call := plan.Calls[i+j]
			if call.Prompt != plan.Calls[i].Prompt || call.PromptSHA256 != plan.Calls[i].PromptSHA256 {
				t.Fatal("candidate prompts differ")
			}
			if call.Route.Model == "MiniMax-M3" && call.Route.ReasoningEffort != "" {
				t.Fatal("M3 effort was fabricated")
			}
			if call.Route.Provider != "minimax" && call.Route.ReasoningEffort != "high" {
				t.Fatal("technical comparison effort changed")
			}
		}
	}
}

func TestUnauthorizedCandidateAndCorpusRejected(t *testing.T) {
	for _, candidate := range []Candidate{"luna-medium", "minimax-m3.1-flash", "openai/gpt-6-luna", ""} {
		if _, err := Route(candidate); err == nil {
			t.Fatalf("unauthorized candidate accepted: %q", candidate)
		}
	}
	short := corpus()[:19]
	duplicate := corpus()
	duplicate[1].ID = duplicate[0].ID
	for _, cases := range [][]Case{short, duplicate, append(corpus(), Case{ID: "extra", Prompt: "extra"})} {
		if _, err := Compile(cases, "synthetic", 4096, ""); err == nil {
			t.Fatal("invalid corpus admitted")
		}
	}
}

func TestBudgetUsesNormalCatalogAndFailsClosed(t *testing.T) {
	if _, err := Compile(corpus(), strings.Repeat("x", 45000), 50000, testPricing); err == nil {
		t.Fatal("over-budget batch admitted")
	}
	if _, err := Compile(corpus(), "synthetic", 4096, `{"version":"unpriced","models":{}}`); err == nil {
		t.Fatal("unpriced batch admitted")
	}
	if _, err := Compile(corpus(), "synthetic", -1, ""); err == nil {
		t.Fatal("negative message bound admitted")
	}
}

func TestMessageDigestBindsRoleOrderAndContent(t *testing.T) {
	a, _ := MessageDigest([]map[string]string{{"role": "system", "content": "safe"}, {"role": "user", "content": "case"}})
	for _, messages := range []any{
		[]map[string]string{{"role": "user", "content": "safe"}, {"role": "user", "content": "case"}},
		[]map[string]string{{"role": "user", "content": "case"}, {"role": "system", "content": "safe"}},
		[]map[string]string{{"role": "system", "content": "safe"}, {"role": "user", "content": "modified"}},
	} {
		b, err := MessageDigest(messages)
		if err != nil || a == b {
			t.Fatal("message mutation did not change digest")
		}
	}
}
