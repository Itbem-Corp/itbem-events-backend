package automationcost

import "testing"

func TestBuildNormalizesCacheAndUsesM3Pricing(t *testing.T) {
	ledger, err := Build("minimax", "MiniMax-M3", map[string]any{
		"prompt_tokens": 1_000_000.0, "completion_tokens": 1_000_000.0, "total_tokens": 2_000_000.0,
		"prompt_tokens_details": map[string]any{"cached_tokens": 500_000.0},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.CachedInputTokens != 500_000 || ledger.InputCostMicros != 300_000 || ledger.OutputCostMicros != 2_400_000 || ledger.CachedCostMicros != 60_000 || ledger.TotalCostMicros != 2_760_000 {
		t.Fatalf("unexpected ledger: %#v", ledger)
	}
	if ledger.PricingBasis != "estimated_api_equivalent" {
		t.Fatalf("unexpected pricing basis: %s", ledger.PricingBasis)
	}
}

func TestBuildRejectsImpossibleCacheUsage(t *testing.T) {
	if _, err := Build("minimax", "MiniMax-M3", map[string]any{"input_tokens": 10.0, "cache_read_input_tokens": 11.0}, ""); err == nil {
		t.Fatal("expected invalid cache usage rejection")
	}
}

func TestBuildDoesNotDoubleCountCacheWritesInFallbackTotal(t *testing.T) {
	ledger, err := Build("minimax", "MiniMax-M2.7", map[string]any{
		"input_tokens": 100.0, "output_tokens": 40.0, "cache_write_tokens": 25.0,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.TotalTokens != 140 {
		t.Fatalf("fallback total = %d, want input + output only", ledger.TotalTokens)
	}
	if _, err := Build("minimax", "MiniMax-M3", map[string]any{
		"input_tokens": 100.0, "output_tokens": 40.0, "total_tokens": 139.0,
	}, ""); err == nil {
		t.Fatal("expected an inconsistent provider total to be rejected")
	}
}

func TestBuildRejectsUsageThatWouldOverflowMicroUSDAccounting(t *testing.T) {
	if _, err := Build("minimax", "MiniMax-M3", map[string]any{
		"input_tokens": maxInt64,
	}, ""); err == nil {
		t.Fatal("expected oversized token usage to fail closed rather than wrap cost")
	}
	if _, err := EstimateUpperBound("minimax", "MiniMax-M3", int(maxInt64), 1, ""); err == nil {
		t.Fatal("expected oversized budget reservation to fail closed rather than wrap cost")
	}
}

func TestBuildNormalizesMiniMaxM3ProviderUsageShape(t *testing.T) {
	ledger, err := Build("minimax", "MiniMax-M3", map[string]any{
		"prompt_tokens":     192.0,
		"completion_tokens": 42.0,
		"total_tokens":      234.0,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 128.0,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": 32.0,
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.InputTokens != 192 || ledger.OutputTokens != 42 || ledger.CachedInputTokens != 128 || ledger.ReasoningTokens != 32 || ledger.TotalTokens != 234 {
		t.Fatalf("unexpected normalized MiniMax usage: %#v", ledger)
	}
	// 64 uncached input, 128 cached input and 42 output tokens; integer
	// micro-USD accounting rounds each independently and stays reproducible.
	if ledger.InputCostMicros != 38 || ledger.CachedCostMicros != 15 || ledger.OutputCostMicros != 101 || ledger.TotalCostMicros != 154 {
		t.Fatalf("unexpected MiniMax M3 ledger cost: %#v", ledger)
	}
}

func TestEstimateUpperBoundUsesHighestInputModeAndMaximumCompletion(t *testing.T) {
	reserved, err := EstimateUpperBound("minimax", "MiniMax-M2.7", 1_000_000, 2_000_000, "")
	if err != nil {
		t.Fatal(err)
	}
	// M2.7 cache write is the most expensive input mode (375,000/million),
	// and output is 1,200,000/million.
	if reserved != 2_775_000 {
		t.Fatalf("reservation = %d, want 2775000", reserved)
	}
	if _, err := EstimateUpperBound("unknown", "model", 1, 1, ""); err == nil {
		t.Fatal("unpriced admission must fail closed")
	}
}
