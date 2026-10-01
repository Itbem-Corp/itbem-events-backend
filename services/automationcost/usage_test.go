package automationcost

import (
	"encoding/json"
	"math"
	"testing"
)

func TestUsageProtocols(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		usage          map[string]any
	}{
		{"chat", "minimax", map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "prompt_tokens_details": map[string]any{"cached_tokens": 80}, "completion_tokens_details": map[string]any{"reasoning_tokens": 30}}},
		{"deepseek", "deepseek", map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20, "reasoning_tokens": 30}},
		{"responses", "openai", map[string]any{"input_tokens": 100, "output_tokens": 40, "input_tokens_details": map[string]any{"cached_tokens": 80}, "output_tokens_details": map[string]any{"reasoning_tokens": 30}}},
		{"router", "openrouter", map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "prompt_tokens_details": map[string]any{"cached_tokens": 80}, "completion_tokens_details": map[string]any{"reasoning_tokens": 30}}},
		{"messages", "anthropic", map[string]any{"input_tokens": 20, "output_tokens": 40, "cache_read_input_tokens": 80, "cache_creation_input_tokens": 0, "reasoning_tokens": 30}},
		{"messages-proxy", "opencode-go", map[string]any{"input_tokens": 20, "output_tokens": 40, "cache_read_input_tokens": 80, "cache_creation_input_tokens": 0, "reasoning_tokens": 30}},
		{"future-chat", "future-provider", map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "prompt_tokens_details": map[string]any{"cached_tokens": 80}, "completion_tokens_details": map[string]any{"reasoning_tokens": 30}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, err := Build(tc.provider, "future-model", TokenUsage(tc.usage), "")
			if err != nil {
				t.Fatal(err)
			}
			if ledger.InputTokens != 100 || ledger.OutputTokens != 40 || ledger.CachedInputTokens != 80 || ledger.ReasoningTokens != 30 || ledger.TotalTokens != 140 {
				t.Fatalf("protocol lost or duplicated usage: %#v", ledger)
			}
			if ledger.PricingBasis != "unpriced" {
				t.Fatal("unknown model must not fabricate a price")
			}
		})
	}
}

func TestUsageRejectsBadNumbers(t *testing.T) {
	for _, value := range []any{-1, 1.5, math.NaN(), math.Inf(1), true, "10", json.Number("1.5"), float64(maxInt64)} {
		for _, usage := range []map[string]any{
			{"prompt_tokens": value, "completion_tokens": 10},
			{"prompt_tokens": 100, "completion_tokens": 10, "prompt_tokens_details": map[string]any{"cached_tokens": value}},
		} {
			if _, err := Build("future-provider", "model", usage, ""); err == nil {
				t.Fatalf("accepted invalid numeric value %v", value)
			}
		}
	}
	if _, err := Build("openai", "model", map[string]any{"input_tokens": 10, "output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 5}}, ""); err == nil {
		t.Fatal("reasoning must be a subset of output, not an additional generation")
	}
}

func TestAggregateMixedShapes(t *testing.T) {
	usage, err := AggregateUsage("deepseek", "future-model",
		map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "prompt_cache_hit_tokens": 80, "reasoning_tokens": 30},
		map[string]any{"input_tokens": 100, "output_tokens": 40, "input_tokens_details": map[string]any{"cached_tokens": 80}, "output_tokens_details": map[string]any{"reasoning_tokens": 30}},
	)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Build("deepseek", "future-model", usage, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.InputTokens != 200 || ledger.OutputTokens != 80 || ledger.CachedInputTokens != 160 || ledger.ReasoningTokens != 60 || ledger.TotalTokens != 280 {
		t.Fatalf("lost aggregated dimensions: %#v", ledger)
	}
}

func TestMessagesAggregateNoDuplication(t *testing.T) {
	call := map[string]any{"input_tokens": 20, "output_tokens": 40, "cache_read_input_tokens": 70, "cache_creation_input_tokens": 10}
	usage, err := AggregateUsage("anthropic", "future-model", call, call)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = AggregateUsage("anthropic", "future-model", usage, call)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := Build("anthropic", "future-model", usage, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.InputTokens != 300 || ledger.CachedInputTokens != 210 || ledger.CacheWriteTokens != 30 || ledger.TotalTokens != 420 {
		t.Fatalf("nested aggregate counted cache twice: %#v", ledger)
	}
}

func TestUsageProjectionIsPrivate(t *testing.T) {
	usage := TokenUsage(map[string]any{"prompt_tokens": 100, "prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20, "output_tokens_details": map[string]any{"reasoning_tokens": 10, "content": "private"}, "prompt": "private"})
	if usage["prompt"] != nil || usage["output_tokens_details"].(map[string]any)["content"] != nil {
		t.Fatal("private response content entered accounting")
	}
	if usage["prompt_cache_hit_tokens"] != 80 {
		t.Fatal("provider cache alias was lost")
	}
}

func TestPartialAggregateStaysUnknown(t *testing.T) {
	usage, err := AggregateUsage("future-provider", "model",
		map[string]any{"input_tokens": 100, "output_tokens": 40, "cached_input_tokens": 80, "reasoning_tokens": 30},
		map[string]any{"input_tokens": 100, "output_tokens": 40},
	)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Profile("future-provider", "model", usage, 100)
	if err != nil {
		t.Fatal(err)
	}
	if profile.InputTokens != 200 || profile.OutputTokens != 80 || profile.CachedInputTokens != nil || profile.ReasoningTokens != nil {
		t.Fatal("partial optional sums were presented as complete usage")
	}
}
