package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// modelsDevCatalogURL is a public, versioned model metadata directory. OpenCode
// itself uses Models.dev for its provider/model directory. It enriches public
// facts only; the authenticated provider /models endpoint remains the source of
// account availability and the execution ledger remains the pricing authority.
const modelsDevCatalogURL = "https://models.dev/api.json"

const maxModelsDevCatalogBytes = 8 << 20

// ProviderModelMetadata is fetched once per synchronization pass and can be
// safely discarded on error. No API key or tenant information is sent to it.
type ProviderModelMetadata struct {
	FetchedAt time.Time
	models    map[Provider]map[string]providerModelMetadata
}

type providerModelMetadata struct {
	ID                       string
	Name                     string
	Description              string
	Family                   string
	ReleaseDate              string
	LastUpdated              string
	KnowledgeCutoff          string
	InputModalities          []string
	OutputModalities         []string
	ContextWindowTokens      int
	MaxOutputTokens          int
	SupportsAttachments      bool
	SupportsReasoning        bool
	ReasoningEfforts         []string
	SupportsTools            bool
	SupportsStructuredOutput bool
	SupportsTemperature      bool
	OpenWeights              bool
	Rates                    providerModelRates
	PricingTiers             []ProviderModelPricingTier
}

type providerModelRates struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Known      bool
}

// ProviderModelPricingTier describes a published alternative token price (for
// example, long-context pricing). It never changes the base execution ledger:
// accounting must use the explicit catalog configuration for that purpose.
type ProviderModelPricingTier struct {
	Kind                       string `json:"kind"`
	ThresholdTokens            int    `json:"threshold_tokens,omitempty"`
	InputMicrosPerMillion      int64  `json:"input_microusd_per_million,omitempty"`
	OutputMicrosPerMillion     int64  `json:"output_microusd_per_million,omitempty"`
	CachedMicrosPerMillion     int64  `json:"cached_microusd_per_million,omitempty"`
	CacheWriteMicrosPerMillion int64  `json:"cache_write_microusd_per_million,omitempty"`
}

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Family           string `json:"family"`
	Attachment       bool   `json:"attachment"`
	Reasoning        bool   `json:"reasoning"`
	ReasoningOptions []struct {
		Type   string   `json:"type"`
		Values []string `json:"values"`
	} `json:"reasoning_options"`
	ToolCall         bool   `json:"tool_call"`
	StructuredOutput bool   `json:"structured_output"`
	Temperature      bool   `json:"temperature"`
	Knowledge        string `json:"knowledge"`
	ReleaseDate      string `json:"release_date"`
	LastUpdated      string `json:"last_updated"`
	OpenWeights      bool   `json:"open_weights"`
	Modalities       struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
		Tiers      []struct {
			Input      float64 `json:"input"`
			Output     float64 `json:"output"`
			CacheRead  float64 `json:"cache_read"`
			CacheWrite float64 `json:"cache_write"`
			Tier       struct {
				Type string `json:"type"`
				Size int    `json:"size"`
			} `json:"tier"`
		} `json:"tiers"`
	} `json:"cost"`
}

func modelsDevProviderID(provider Provider) string {
	if provider == ProviderOpenCodeGo {
		return "opencode-go"
	}
	return string(provider)
}

// FetchProviderModelMetadata loads bounded public data once. A failed or
// malformed directory is non-fatal: callers retain live provider information.
func FetchProviderModelMetadata(ctx context.Context, client *http.Client) (*ProviderModelMetadata, error) {
	if ctx == nil {
		return nil, fmt.Errorf("model metadata context is unavailable")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevCatalogURL, nil)
	if err != nil {
		return nil, fmt.Errorf("model metadata request is unavailable")
	}
	response, err := client.Do(req)
	if err != nil || response == nil {
		return nil, fmt.Errorf("model metadata request is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("model metadata request failed")
	}
	var document map[string]modelsDevProvider
	if err := json.NewDecoder(io.LimitReader(response.Body, maxModelsDevCatalogBytes)).Decode(&document); err != nil {
		return nil, fmt.Errorf("model metadata response is invalid")
	}
	metadata := &ProviderModelMetadata{FetchedAt: time.Now().UTC(), models: make(map[Provider]map[string]providerModelMetadata)}
	for _, provider := range cataloguedProviderMetadataTargets() {
		entry, ok := document[modelsDevProviderID(provider)]
		if !ok {
			continue
		}
		models := make(map[string]providerModelMetadata, len(entry.Models))
		for key, raw := range entry.Models {
			model := normalizeModelsDevModel(key, raw)
			if model.ID != "" {
				models[strings.ToLower(model.ID)] = model
			}
		}
		metadata.models[provider] = models
	}
	return metadata, nil
}

// Kept local to avoid an import cycle with controllers/automation.
func cataloguedProviderMetadataTargets() []Provider {
	return []Provider{ProviderMiniMax, ProviderDeepSeek, ProviderOpenRouter, ProviderOpenAI, ProviderAnthropic, ProviderOpenCodeGo}
}

func normalizeModelsDevModel(key string, raw modelsDevModel) providerModelMetadata {
	id := strings.TrimSpace(raw.ID)
	if id == "" {
		id = strings.TrimSpace(key)
	}
	if id == "" || len(id) > 200 {
		return providerModelMetadata{}
	}
	reasoningEfforts := make([]string, 0)
	for _, option := range raw.ReasoningOptions {
		if strings.EqualFold(strings.TrimSpace(option.Type), "effort") {
			reasoningEfforts = append(reasoningEfforts, option.Values...)
		}
	}
	tiers := make([]ProviderModelPricingTier, 0, len(raw.Cost.Tiers))
	for _, tier := range raw.Cost.Tiers {
		result := ProviderModelPricingTier{
			Kind:                       strings.TrimSpace(tier.Tier.Type),
			ThresholdTokens:            nonNegative(tier.Tier.Size),
			InputMicrosPerMillion:      priceDollarsPerMillionToMicros(tier.Input),
			OutputMicrosPerMillion:     priceDollarsPerMillionToMicros(tier.Output),
			CachedMicrosPerMillion:     priceDollarsPerMillionToMicros(tier.CacheRead),
			CacheWriteMicrosPerMillion: priceDollarsPerMillionToMicros(tier.CacheWrite),
		}
		if result.Kind != "" && (result.InputMicrosPerMillion > 0 || result.OutputMicrosPerMillion > 0 || result.CachedMicrosPerMillion > 0 || result.CacheWriteMicrosPerMillion > 0) {
			tiers = append(tiers, result)
		}
	}
	sort.Slice(tiers, func(i, j int) bool { return tiers[i].ThresholdTokens < tiers[j].ThresholdTokens })
	rates := providerModelRates{
		Input:      priceDollarsPerMillionToMicros(raw.Cost.Input),
		Output:     priceDollarsPerMillionToMicros(raw.Cost.Output),
		CacheRead:  priceDollarsPerMillionToMicros(raw.Cost.CacheRead),
		CacheWrite: priceDollarsPerMillionToMicros(raw.Cost.CacheWrite),
	}
	rates.Known = rates.Input > 0 || rates.Output > 0 || rates.CacheRead > 0 || rates.CacheWrite > 0
	return providerModelMetadata{
		ID: id, Name: strings.TrimSpace(raw.Name), Description: sanitizeModelDescription(raw.Description), Family: strings.TrimSpace(raw.Family), ReleaseDate: strings.TrimSpace(raw.ReleaseDate), LastUpdated: strings.TrimSpace(raw.LastUpdated), KnowledgeCutoff: strings.TrimSpace(raw.Knowledge),
		InputModalities: sanitizeModelCapabilities(raw.Modalities.Input), OutputModalities: sanitizeModelCapabilities(raw.Modalities.Output), ContextWindowTokens: nonNegative(raw.Limit.Context), MaxOutputTokens: nonNegative(raw.Limit.Output), SupportsAttachments: raw.Attachment, SupportsReasoning: raw.Reasoning, ReasoningEfforts: sanitizeModelCapabilities(reasoningEfforts), SupportsTools: raw.ToolCall, SupportsStructuredOutput: raw.StructuredOutput, SupportsTemperature: raw.Temperature, OpenWeights: raw.OpenWeights, Rates: rates, PricingTiers: tiers,
	}
}

func priceDollarsPerMillionToMicros(value float64) int64 {
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(math.MaxInt64)/1e6 {
		return 0
	}
	return int64(math.Round(value * 1e6))
}

func (metadata *ProviderModelMetadata) model(provider Provider, id string) (providerModelMetadata, bool) {
	if metadata == nil {
		return providerModelMetadata{}, false
	}
	models := metadata.models[provider]
	model, ok := models[strings.ToLower(strings.TrimSpace(id))]
	return model, ok
}

func (metadata *ProviderModelMetadata) modelsFor(provider Provider) []providerModelMetadata {
	if metadata == nil {
		return nil
	}
	models := metadata.models[provider]
	result := make([]providerModelMetadata, 0, len(models))
	for _, model := range models {
		result = append(result, model)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
