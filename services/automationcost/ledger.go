// Package automationcost normalizes provider usage and applies the server-owned
// price catalog. It never receives a provider secret, prompt, or completion.
package automationcost

import (
	"encoding/json"
	"fmt"
	"strings"
)

const perMillion = int64(1_000_000)
const maxInt64 = int64(^uint64(0) >> 1)

type Rates struct {
	InputMicrosPerMillion      int64 `json:"input_microusd_per_million"`
	OutputMicrosPerMillion     int64 `json:"output_microusd_per_million"`
	CachedMicrosPerMillion     int64 `json:"cached_microusd_per_million"`
	CacheWriteMicrosPerMillion int64 `json:"cache_write_microusd_per_million"`
}

type Catalog struct {
	Version string           `json:"version"`
	Basis   string           `json:"basis"`
	Models  map[string]Rates `json:"models"`
}

type Ledger struct {
	InputTokens          int64
	OutputTokens         int64
	CachedInputTokens    int64
	CacheWriteTokens     int64
	ReasoningTokens      int64
	TotalTokens          int64
	InputCostMicros      int64
	OutputCostMicros     int64
	CachedCostMicros     int64
	CacheWriteCostMicros int64
	TotalCostMicros      int64
	PricingBasis         string
	PricingSnapshot      string
}

// Build derives a reproducible financial record from the raw provider usage.
// When an organization is on a subscription plan, the default is deliberately
// labeled API-equivalent: it is useful for internal allocation but never
// presented as an invoice. Deployments can replace it with their own catalog.
func Build(provider, model string, usage map[string]any, configured string) (Ledger, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if provider == "" || model == "" {
		return Ledger{}, fmt.Errorf("provider and model are required for cost accounting")
	}
	if err := ValidateTokenUsage(usage); err != nil {
		return Ledger{}, err
	}
	catalog, err := catalogFor(configured)
	if err != nil {
		return Ledger{}, err
	}
	ledger := Ledger{
		InputTokens:       usageNumber(usage, "input_tokens", "prompt_tokens", "prompt_token_count"),
		OutputTokens:      usageNumber(usage, "output_tokens", "completion_tokens", "completion_token_count"),
		CachedInputTokens: usageNumber(usage, "cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens", "prompt_cache_hit_tokens"),
		CacheWriteTokens:  usageNumber(usage, "cache_creation_input_tokens", "cache_write_tokens"),
		ReasoningTokens:   usageNumber(usage, "reasoning_tokens", "thinking_tokens"),
		TotalTokens:       usageNumber(usage, "total_tokens"),
		PricingBasis:      "unpriced",
	}
	if nested := mapValue(usage["prompt_tokens_details"]); ledger.CachedInputTokens == 0 {
		ledger.CachedInputTokens = usageNumber(nested, "cached_tokens", "cache_read_input_tokens")
	}
	if nested := mapValue(usage["input_tokens_details"]); ledger.CachedInputTokens == 0 {
		ledger.CachedInputTokens = usageNumber(nested, "cached_tokens", "cache_read_input_tokens")
	}
	if nested := mapValue(usage["prompt_tokens_details"]); ledger.CacheWriteTokens == 0 {
		ledger.CacheWriteTokens = usageNumber(nested, "cache_write_tokens", "cache_creation_input_tokens", "cache_creation_tokens")
	}
	if nested := mapValue(usage["input_tokens_details"]); ledger.CacheWriteTokens == 0 {
		ledger.CacheWriteTokens = usageNumber(nested, "cache_write_tokens", "cache_creation_input_tokens", "cache_creation_tokens")
	}
	// MiniMax M3 reports reasoning usage inside completion_tokens_details. Keep
	// it visible in the immutable ledger even when the provider bills it as part
	// of completion tokens, so operators can distinguish answer size from model
	// deliberation without changing the financial total.
	if nested := mapValue(usage["completion_tokens_details"]); ledger.ReasoningTokens == 0 {
		ledger.ReasoningTokens = usageNumber(nested, "reasoning_tokens", "thinking_tokens")
	}
	if nested := mapValue(usage["output_tokens_details"]); ledger.ReasoningTokens == 0 {
		ledger.ReasoningTokens = usageNumber(nested, "reasoning_tokens", "thinking_tokens")
	}
	// Native Messages usage excludes cache reads/writes from input_tokens.
	// OpenAI-compatible input/prompt totals already include those dimensions.
	if (provider == "anthropic" || provider == "opencode-go") && usage["cached_input_tokens"] == nil && usage["prompt_tokens"] == nil && usage["input_tokens_details"] == nil && (usage["cache_creation_input_tokens"] != nil || usage["cache_read_input_tokens"] != nil) {
		ledger.InputTokens, err = sumNonNegative(ledger.InputTokens, ledger.CachedInputTokens, ledger.CacheWriteTokens)
		if err != nil {
			return Ledger{}, err
		}
	}
	if ledger.ReasoningTokens > ledger.OutputTokens {
		return Ledger{}, fmt.Errorf("provider reasoning tokens exceed output tokens")
	}
	if hit, hitReported := usage["prompt_cache_hit_tokens"]; hitReported {
		if miss, missReported := usage["prompt_cache_miss_tokens"]; missReported {
			hitCount, _ := tokenCount(hit)
			missCount, _ := tokenCount(miss)
			input, err := sumNonNegative(hitCount, missCount)
			if err != nil || input != ledger.InputTokens {
				return Ledger{}, fmt.Errorf("provider cache hits and misses do not reconcile with input")
			}
		}
	}
	minimumTotal, err := sumNonNegative(ledger.InputTokens, ledger.OutputTokens)
	if err != nil {
		return Ledger{}, fmt.Errorf("provider usage exceeds the supported token range")
	}
	if ledger.TotalTokens == 0 {
		// Cache reads and cache writes are dimensions of provider input, not an
		// additional generation. Keep the fallback total reconciled with the
		// canonical input/output aggregate and never count cache writes twice.
		ledger.TotalTokens = minimumTotal
	}
	cacheInput, err := sumNonNegative(ledger.CachedInputTokens, ledger.CacheWriteTokens)
	if err != nil || ledger.InputTokens < cacheInput {
		return Ledger{}, fmt.Errorf("provider usage cache tokens exceed input tokens")
	}
	if ledger.TotalTokens < minimumTotal {
		return Ledger{}, fmt.Errorf("provider usage total tokens are inconsistent with input and output")
	}
	rates, priced := catalog.Models[provider+":"+model]
	if !priced {
		rates, priced = catalog.Models[provider+":*"]
	}
	if priced {
		billableInput := ledger.InputTokens - ledger.CachedInputTokens - ledger.CacheWriteTokens
		var costErr error
		if ledger.InputCostMicros, costErr = cost(billableInput, rates.InputMicrosPerMillion); costErr != nil {
			return Ledger{}, costErr
		}
		if ledger.OutputCostMicros, costErr = cost(ledger.OutputTokens, rates.OutputMicrosPerMillion); costErr != nil {
			return Ledger{}, costErr
		}
		if ledger.CachedCostMicros, costErr = cost(ledger.CachedInputTokens, rates.CachedMicrosPerMillion); costErr != nil {
			return Ledger{}, costErr
		}
		if ledger.CacheWriteCostMicros, costErr = cost(ledger.CacheWriteTokens, rates.CacheWriteMicrosPerMillion); costErr != nil {
			return Ledger{}, costErr
		}
		if ledger.TotalCostMicros, costErr = sumCost(ledger.InputCostMicros, ledger.OutputCostMicros, ledger.CachedCostMicros, ledger.CacheWriteCostMicros); costErr != nil {
			return Ledger{}, costErr
		}
		ledger.PricingBasis = catalog.Basis
	}
	snapshot, err := json.Marshal(map[string]any{
		"catalog_version":            catalog.Version,
		"basis":                      catalog.Basis,
		"provider":                   provider,
		"model":                      model,
		"rates_microusd_per_million": rates,
	})
	if err != nil {
		return Ledger{}, err
	}
	ledger.PricingSnapshot = string(snapshot)
	return ledger, nil
}

// EstimateUpperBound returns a conservative financial admission hold for a
// request before the provider is called. InputBytes is deliberately treated
// as an upper bound on input tokens; a UTF-8 token cannot contain more tokens
// than source bytes. The input rate uses the most expensive supported cache
// mode, so a later provider cache classification cannot exceed the hold.
func EstimateUpperBound(provider, model string, inputBytes, maxCompletionTokens int, configured string) (int64, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	if provider == "" || model == "" || inputBytes < 0 || maxCompletionTokens < 0 {
		return 0, fmt.Errorf("provider, model and non-negative token bounds are required for budget admission")
	}
	catalog, err := catalogFor(configured)
	if err != nil {
		return 0, err
	}
	rates, priced := catalog.Models[provider+":"+model]
	if !priced {
		rates, priced = catalog.Models[provider+":*"]
	}
	if !priced {
		return 0, fmt.Errorf("no pricing catalog entry exists for budget admission")
	}
	inputRate := rates.InputMicrosPerMillion
	if rates.CachedMicrosPerMillion > inputRate {
		inputRate = rates.CachedMicrosPerMillion
	}
	if rates.CacheWriteMicrosPerMillion > inputRate {
		inputRate = rates.CacheWriteMicrosPerMillion
	}
	inputCost, err := cost(int64(inputBytes), inputRate)
	if err != nil {
		return 0, err
	}
	outputCost, err := cost(int64(maxCompletionTokens), rates.OutputMicrosPerMillion)
	if err != nil {
		return 0, err
	}
	return sumCost(inputCost, outputCost)
}

func catalogFor(configured string) (Catalog, error) {
	if strings.TrimSpace(configured) == "" {
		return Catalog{Version: "official-text-prices-2026-09-23", Basis: "official_api_price", Models: map[string]Rates{
			// Sources checked 2026-09-23: MiniMax Token Plan, DeepSeek Models &
			// Pricing, and OpenAI API Pricing. All amounts are micro-USD / 1M.
			"minimax:minimax-m3":             {InputMicrosPerMillion: 600000, OutputMicrosPerMillion: 2400000, CachedMicrosPerMillion: 120000},
			"minimax:minimax-m2.7":           {InputMicrosPerMillion: 300000, OutputMicrosPerMillion: 1200000, CachedMicrosPerMillion: 60000, CacheWriteMicrosPerMillion: 375000},
			"minimax:minimax-m2.7-highspeed": {InputMicrosPerMillion: 600000, OutputMicrosPerMillion: 2400000, CachedMicrosPerMillion: 60000, CacheWriteMicrosPerMillion: 375000},
			// DeepSeek switches between peak and off-peak pricing. The built-in
			// catalogue intentionally uses its published peak rate so a hard budget
			// admission never under-reserves. Deployments that need invoice-exact
			// off-peak accounting can provide a time-aware external price catalog.
			"deepseek:deepseek-flash":  {InputMicrosPerMillion: 300000, OutputMicrosPerMillion: 1200000, CachedMicrosPerMillion: 6000},
			"deepseek:deepseek-v4-pro": {InputMicrosPerMillion: 1320000, OutputMicrosPerMillion: 3960000, CachedMicrosPerMillion: 44000},
			"openai:gpt-6-astra":       {InputMicrosPerMillion: 5000000, OutputMicrosPerMillion: 25000000, CachedMicrosPerMillion: 500000, CacheWriteMicrosPerMillion: 6250000},
			"openai:gpt-6-sol":         {InputMicrosPerMillion: 1000000, OutputMicrosPerMillion: 5000000, CachedMicrosPerMillion: 100000, CacheWriteMicrosPerMillion: 1250000},
			"openai:gpt-6-luna":        {InputMicrosPerMillion: 50000, OutputMicrosPerMillion: 250000, CachedMicrosPerMillion: 5000, CacheWriteMicrosPerMillion: 62500},
			"openai:gpt-5.3-codex":     {InputMicrosPerMillion: 1750000, OutputMicrosPerMillion: 14000000, CachedMicrosPerMillion: 175000},
			// Anthropic publishes 5-minute prompt-cache write/read rates. These
			// exact dated IDs remain useful even after a newer live model appears;
			// unknown IDs deliberately remain unpriced until reviewed.
			"anthropic:claude-opus-4-1-20250805":   {InputMicrosPerMillion: 15000000, OutputMicrosPerMillion: 75000000, CachedMicrosPerMillion: 1500000, CacheWriteMicrosPerMillion: 18750000},
			"anthropic:claude-opus-4-20250514":     {InputMicrosPerMillion: 15000000, OutputMicrosPerMillion: 75000000, CachedMicrosPerMillion: 1500000, CacheWriteMicrosPerMillion: 18750000},
			"anthropic:claude-sonnet-4-20250514":   {InputMicrosPerMillion: 3000000, OutputMicrosPerMillion: 15000000, CachedMicrosPerMillion: 300000, CacheWriteMicrosPerMillion: 3750000},
			"anthropic:claude-3-7-sonnet-20250219": {InputMicrosPerMillion: 3000000, OutputMicrosPerMillion: 15000000, CachedMicrosPerMillion: 300000, CacheWriteMicrosPerMillion: 3750000},
			"anthropic:claude-3-5-haiku-20241022":  {InputMicrosPerMillion: 800000, OutputMicrosPerMillion: 4000000, CachedMicrosPerMillion: 80000, CacheWriteMicrosPerMillion: 1000000},
		}}, nil
	}
	var catalog Catalog
	if err := json.Unmarshal([]byte(configured), &catalog); err != nil {
		return Catalog{}, fmt.Errorf("AUTOMATION_PRICING_JSON must be valid JSON")
	}
	if strings.TrimSpace(catalog.Version) == "" || strings.TrimSpace(catalog.Basis) == "" || len(catalog.Models) == 0 {
		return Catalog{}, fmt.Errorf("AUTOMATION_PRICING_JSON requires version, basis and models")
	}
	for key, rate := range catalog.Models {
		if strings.TrimSpace(key) == "" || rate.InputMicrosPerMillion < 0 || rate.OutputMicrosPerMillion < 0 || rate.CachedMicrosPerMillion < 0 || rate.CacheWriteMicrosPerMillion < 0 {
			return Catalog{}, fmt.Errorf("AUTOMATION_PRICING_JSON contains invalid model rates")
		}
	}
	return catalog, nil
}

// RatesFor returns the exact configured model (or provider wildcard) rate
// without exposing mutable catalog internals. It is used by the safe model
// catalogue projection so the dashboard can show the same price basis that
// the immutable execution ledger will use.
func RatesFor(provider, model, configured string) (Rates, bool, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	catalog, err := catalogFor(configured)
	if err != nil {
		return Rates{}, false, err
	}
	rates, ok := catalog.Models[provider+":"+model]
	if !ok {
		rates, ok = catalog.Models[provider+":*"]
	}
	return rates, ok, nil
}

// cost keeps the immutable micro-USD ledger inside signed 64-bit range. A
// provider report outside this mathematical range is invalid accounting, not
// a value to wrap, clamp or silently undercharge.
func cost(tokens, rate int64) (int64, error) {
	if tokens <= 0 || rate <= 0 {
		return 0, nil
	}
	if tokens > (maxInt64-perMillion/2)/rate {
		return 0, fmt.Errorf("token usage exceeds the supported cost range")
	}
	return (tokens*rate + perMillion/2) / perMillion, nil
}

func sumCost(values ...int64) (int64, error) {
	return sumNonNegative(values...)
}

func sumNonNegative(values ...int64) (int64, error) {
	var total int64
	for _, value := range values {
		if value < 0 || total > maxInt64-value {
			return 0, fmt.Errorf("token usage exceeds the supported cost range")
		}
		total += value
	}
	return total, nil
}

func usageNumber(usage map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := usage[key].(type) {
		case float64:
			if value >= 0 {
				return int64(value)
			}
		case int64:
			if value >= 0 {
				return value
			}
		case int:
			if value >= 0 {
				return int64(value)
			}
		case json.Number:
			if result, err := value.Int64(); err == nil && result >= 0 {
				return result
			}
		}
	}
	return 0
}

func mapValue(value any) map[string]any { result, _ := value.(map[string]any); return result }
