package automationcost

// UsageProfile is a numeric-only projection for every gateway call. Ratios name
// their denominator explicitly. Missing optional counters remain null; reasoning
// is included in output and is never billed or added to the total twice.
type UsageProfile struct {
	InputTokens            int64    `json:"input_tokens"`
	OutputTokens           int64    `json:"output_tokens"`
	CachedInputTokens      *int64   `json:"cached_input_tokens"`
	CacheWriteTokens       *int64   `json:"cache_write_tokens"`
	ReasoningTokens        *int64   `json:"reasoning_tokens"`
	AnswerTokens           *int64   `json:"answer_tokens"`
	CacheHitInputPercent   *float64 `json:"cache_hit_input_percent"`
	ReasoningOutputPercent *float64 `json:"reasoning_output_percent"`
	OutputTotalPercent     float64  `json:"output_total_percent"`
	OutputBudgetPercent    float64  `json:"output_budget_percent"`
	NearOutputLimit        bool     `json:"near_output_limit"`
}

func Profile(provider, model string, usage map[string]any, maxOutput int) (*UsageProfile, error) {
	if err := VerifyTokenUsage(usage); err != nil {
		return nil, err
	}
	ledger, err := Build(provider, model, usage, usageOnlyCatalog)
	if err != nil {
		return nil, err
	}
	profile := &UsageProfile{InputTokens: ledger.InputTokens, OutputTokens: ledger.OutputTokens}
	if usageReported(usage, []string{"cache_write_tokens", "cache_creation_input_tokens", "cache_creation_tokens"}, []string{"prompt_tokens_details", "input_tokens_details"}) {
		profile.CacheWriteTokens = &ledger.CacheWriteTokens
	}
	if ledger.TotalTokens > 0 {
		profile.OutputTotalPercent = 100 * float64(ledger.OutputTokens) / float64(ledger.TotalTokens)
	}
	if maxOutput > 0 {
		profile.OutputBudgetPercent = 100 * float64(ledger.OutputTokens) / float64(maxOutput)
		profile.NearOutputLimit = profile.OutputBudgetPercent >= 90
	}
	if cacheReported(usage) {
		profile.CachedInputTokens = &ledger.CachedInputTokens
		if ledger.InputTokens > 0 {
			percent := 100 * float64(ledger.CachedInputTokens) / float64(ledger.InputTokens)
			profile.CacheHitInputPercent = &percent
		}
	}
	if reasoningReported(usage) {
		profile.ReasoningTokens = &ledger.ReasoningTokens
		answer := ledger.OutputTokens - ledger.ReasoningTokens
		profile.AnswerTokens = &answer
		if ledger.OutputTokens > 0 {
			percent := 100 * float64(ledger.ReasoningTokens) / float64(ledger.OutputTokens)
			profile.ReasoningOutputPercent = &percent
		}
	}
	return profile, nil
}
