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
	catalog, err := catalogFor(configured)
	if err != nil {
		return Ledger{}, err
	}
	ledger := Ledger{
		InputTokens:       usageNumber(usage, "input_tokens", "prompt_tokens", "prompt_token_count"),
		OutputTokens:      usageNumber(usage, "output_tokens", "completion_tokens", "completion_token_count"),
		CachedInputTokens: usageNumber(usage, "cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens"),
		CacheWriteTokens:  usageNumber(usage, "cache_creation_input_tokens", "cache_write_tokens"),
		ReasoningTokens:   usageNumber(usage, "reasoning_tokens", "thinking_tokens"),
		TotalTokens:       usageNumber(usage, "total_tokens"),
		PricingBasis:      "unpriced",
	}
	if nested := mapValue(usage["prompt_tokens_details"]); ledger.CachedInputTokens == 0 {
		ledger.CachedInputTokens = usageNumber(nested, "cached_tokens", "cache_read_input_tokens")
	}
	// MiniMax M3 reports reasoning usage inside completion_tokens_details. Keep
	// it visible in the immutable ledger even when the provider bills it as part
	// of completion tokens, so operators can distinguish answer size from model
	// deliberation without changing the financial total.
	if nested := mapValue(usage["completion_tokens_details"]); ledger.ReasoningTokens == 0 {
		ledger.ReasoningTokens = usageNumber(nested, "reasoning_tokens", "thinking_tokens")
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
		return Catalog{Version: "minimax-paygo-2026-08", Basis: "estimated_api_equivalent", Models: map[string]Rates{
			// Source: MiniMax public pay-as-you-go page (checked 2026-08-08).
			"minimax:minimax-m3":   {InputMicrosPerMillion: 600000, OutputMicrosPerMillion: 2400000, CachedMicrosPerMillion: 120000},
			"minimax:minimax-m2.7": {InputMicrosPerMillion: 300000, OutputMicrosPerMillion: 1200000, CachedMicrosPerMillion: 60000, CacheWriteMicrosPerMillion: 375000},
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
