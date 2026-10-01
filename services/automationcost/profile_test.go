package automationcost

import "testing"

func TestProfileSeparatesDenominators(t *testing.T) {
	profile, err := Profile("deepseek", "future-model", map[string]any{
		"prompt_tokens": 24831, "completion_tokens": 31670, "total_tokens": 56501,
		"prompt_cache_hit_tokens": 24576, "prompt_cache_miss_tokens": 255,
		"completion_tokens_details": map[string]any{"reasoning_tokens": 30781},
	}, 32768)
	if err != nil {
		t.Fatal(err)
	}
	if *profile.CacheHitInputPercent < 98 || profile.OutputTotalPercent < 56 || *profile.ReasoningOutputPercent < 97 || *profile.AnswerTokens != 889 || !profile.NearOutputLimit {
		t.Fatalf("misleading consumption profile: %#v", profile)
	}
}

func TestProfileUnknownCountersStayUnknown(t *testing.T) {
	profile, err := Profile("future-provider", "future-model", map[string]any{"input_tokens": 10, "output_tokens": 4}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if profile.CachedInputTokens != nil || profile.CacheHitInputPercent != nil || profile.ReasoningTokens != nil || profile.AnswerTokens != nil || profile.ReasoningOutputPercent != nil {
		t.Fatal("unreported optional counters must remain unknown")
	}
	profile, err = Profile("future-provider", "future-model", map[string]any{"input_tokens": 10, "output_tokens": 4, "reasoning_tokens": 0, "cached_input_tokens": 0}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ReasoningTokens == nil || *profile.ReasoningTokens != 0 || *profile.AnswerTokens != 4 || profile.CachedInputTokens == nil {
		t.Fatal("reported zero must remain distinguishable from missing")
	}
}

func TestVerifiedUsageNeedsBothCounters(t *testing.T) {
	for _, usage := range []map[string]any{{"input_tokens": 10}, {"output_tokens": 10}, {"total_tokens": 10}} {
		if err := VerifyTokenUsage(usage); err == nil {
			t.Fatal("incomplete accounting became verified")
		}
	}
	if err := VerifyTokenUsage(map[string]any{"input_tokens": 10, "output_tokens": 0}); err != nil {
		t.Fatal(err)
	}
}

func TestUsageAliasesReconcile(t *testing.T) {
	for _, usage := range []map[string]any{
		{"prompt_tokens": 10, "input_tokens": 11, "output_tokens": 2},
		{"prompt_tokens": 10, "output_tokens": 2, "prompt_cache_hit_tokens": 8, "prompt_tokens_details": map[string]any{"cached_tokens": 7}},
		{"prompt_tokens": 10, "output_tokens": 2, "prompt_cache_hit_tokens": 8, "prompt_cache_miss_tokens": 3},
	} {
		if _, err := Build("deepseek", "future-model", usage, ""); err == nil {
			t.Fatal("contradictory provider counters were accepted")
		}
	}
}
