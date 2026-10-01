package automationcost

import (
	"encoding/json"
	"fmt"
	"math"
)

// These are accounting dimensions, never arbitrary provider response data.
var usageKeys = []string{
	"input_tokens", "prompt_tokens", "prompt_token_count", "output_tokens", "completion_tokens", "completion_token_count", "total_tokens",
	"cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens", "cache_creation_input_tokens", "cache_write_tokens",
	"prompt_cache_hit_tokens", "prompt_cache_miss_tokens", "reasoning_tokens", "thinking_tokens",
}

var usageDetails = map[string][]string{
	"prompt_tokens_details":     {"cached_tokens", "cache_read_input_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_creation_tokens"},
	"input_tokens_details":      {"cached_tokens", "cache_read_input_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_creation_tokens"},
	"completion_tokens_details": {"reasoning_tokens", "thinking_tokens"},
	"output_tokens_details":     {"reasoning_tokens", "thinking_tokens"},
}

const usageOnlyCatalog = `{"version":"usage-only","basis":"unpriced","models":{"usage-only:*":{}}}`

func usageReported(raw map[string]any, keys, parents []string) bool {
	for _, key := range keys {
		if _, ok := raw[key]; ok {
			return true
		}
	}
	for _, parent := range parents {
		for _, key := range keys {
			if _, ok := mapValue(raw[parent])[key]; ok {
				return true
			}
		}
	}
	return false
}

func cacheReported(raw map[string]any) bool {
	return usageReported(raw, []string{"cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens", "prompt_cache_hit_tokens", "cached_tokens"}, []string{"prompt_tokens_details", "input_tokens_details"})
}

func reasoningReported(raw map[string]any) bool {
	return usageReported(raw, []string{"reasoning_tokens", "thinking_tokens"}, []string{"completion_tokens_details", "output_tokens_details"})
}

// VerifyTokenUsage requires explicit input and output dimensions, including
// reported zero. A missing output counter is unknown, not free generation.
func VerifyTokenUsage(raw map[string]any) error {
	if err := ValidateTokenUsage(raw); err != nil {
		return err
	}
	for _, keys := range [][]string{{"input_tokens", "prompt_tokens", "prompt_token_count"}, {"output_tokens", "completion_tokens", "completion_token_count"}} {
		found := false
		for _, key := range keys {
			if _, exists := raw[key]; exists {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("provider usage is missing input or output accounting")
		}
	}
	return nil
}

func tokenCount(raw any) (int64, bool) {
	switch value := raw.(type) {
	case int:
		return int64(value), value >= 0
	case int64:
		return value, value >= 0
	case float64:
		if value >= 0 && value < float64(maxInt64) && !math.IsInf(value, 0) && math.Trunc(value) == value {
			return int64(value), true
		}
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil && parsed >= 0
	}
	return 0, false
}

// TokenUsage preserves only finite, non-negative integer accounting fields.
// Use ValidateTokenUsage first when accepting a financial receipt: invalid
// known fields must not silently turn into a verified zero.
func TokenUsage(raw map[string]any) map[string]any {
	clean := map[string]any{}
	for _, key := range usageKeys {
		if value, exists := raw[key]; exists {
			if _, valid := tokenCount(value); valid {
				clean[key] = value
			}
		}
	}
	for parent, keys := range usageDetails {
		child, ok := raw[parent].(map[string]any)
		if !ok {
			continue
		}
		filtered := map[string]any{}
		for _, key := range keys {
			if value, exists := child[key]; exists {
				if _, valid := tokenCount(value); valid {
					filtered[key] = value
				}
			}
		}
		if len(filtered) > 0 {
			clean[parent] = filtered
		}
	}
	return clean
}

func ValidateTokenUsage(raw map[string]any) error {
	validate := func(values map[string]any, keys []string) error {
		for _, key := range keys {
			if value, exists := values[key]; exists {
				if _, valid := tokenCount(value); !valid {
					return fmt.Errorf("invalid provider token count: %s", key)
				}
			}
		}
		return nil
	}
	if err := validate(raw, usageKeys); err != nil {
		return err
	}
	for parent, keys := range usageDetails {
		if value, exists := raw[parent]; exists && value != nil {
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid provider token details: %s", parent)
			}
			if err := validate(child, keys); err != nil {
				return err
			}
		}
	}
	for _, dimension := range []struct{ keys, parents []string }{
		{[]string{"input_tokens", "prompt_tokens", "prompt_token_count"}, nil},
		{[]string{"output_tokens", "completion_tokens", "completion_token_count"}, nil},
		{[]string{"cache_read_input_tokens", "cached_input_tokens", "cache_read_tokens", "prompt_cache_hit_tokens", "cached_tokens"}, []string{"input_tokens_details", "prompt_tokens_details"}},
		{[]string{"cache_creation_input_tokens", "cache_write_tokens", "cache_creation_tokens"}, []string{"input_tokens_details", "prompt_tokens_details"}},
		{[]string{"reasoning_tokens", "thinking_tokens"}, []string{"output_tokens_details", "completion_tokens_details"}},
	} {
		var previous int64
		found := false
		maps := []map[string]any{raw}
		for _, parent := range dimension.parents {
			maps = append(maps, mapValue(raw[parent]))
		}
		for _, values := range maps {
			for _, key := range dimension.keys {
				if value, exists := values[key]; exists {
					count, valid := tokenCount(value)
					if !valid {
						return fmt.Errorf("invalid provider token count: %s", key)
					}
					if found && count != previous {
						return fmt.Errorf("conflicting provider token aliases: %s", key)
					}
					previous, found = count, true
				}
			}
		}
	}
	return nil
}

// AggregateUsage normalizes each call before summing. Summing provider aliases
// directly loses nested details and undercounts when calls use different shapes.
// This is audit metadata; immutable per-call receipts remain the billing source.
func AggregateUsage(provider, model string, calls ...map[string]any) (map[string]any, error) {
	result := map[string]any{}
	cacheKnown, reasoningKnown, writesKnown := true, true, true
	for _, usage := range calls {
		cacheKnown = cacheKnown && cacheReported(usage)
		reasoningKnown = reasoningKnown && reasoningReported(usage)
		writesKnown = writesKnown && usageReported(usage, []string{"cache_write_tokens", "cache_creation_input_tokens", "cache_creation_tokens"}, []string{"prompt_tokens_details", "input_tokens_details"})
		ledger, err := Build(provider, model, usage, usageOnlyCatalog)
		if err != nil {
			return nil, err
		}
		counts := map[string]int64{
			"input_tokens": ledger.InputTokens, "output_tokens": ledger.OutputTokens, "total_tokens": ledger.TotalTokens,
			"cached_input_tokens": ledger.CachedInputTokens, "cache_write_tokens": ledger.CacheWriteTokens, "reasoning_tokens": ledger.ReasoningTokens,
		}
		for key, count := range counts {
			previous, _ := tokenCount(result[key])
			total, err := sumNonNegative(previous, count)
			if err != nil {
				return nil, err
			}
			result[key] = total
		}
	}
	// A partial sum is not the group's verified total. Original call usage
	// remains available in the private audit and immutable financial receipts.
	if !cacheKnown {
		delete(result, "cached_input_tokens")
	}
	if !reasoningKnown {
		delete(result, "reasoning_tokens")
	}
	if !writesKnown {
		delete(result, "cache_write_tokens")
	}
	return result, nil
}
