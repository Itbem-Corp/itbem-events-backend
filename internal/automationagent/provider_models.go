package automationagent

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"events-stocks/services/automationcost"
)

const maxProviderModels = 512

const (
	modelLimitsCacheTTL        = 5 * time.Minute
	modelLimitsResolveTimeout  = 15 * time.Second
	maxModelLimitsCacheEntries = 2048
)

const miniMaxTokenPlanRemainsURL = "https://www.minimax.io/v1/token_plan/remains"

// ProviderModel is intentionally a credential-free projection that can be
// returned to a primary platform administrator. Prices are optional provider
// catalogue hints, never an authorization or accounting source.
type ProviderModel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Publisher and Family are stable catalog taxonomy fields used only for
	// presentation and filtering. They are never treated as an execution route.
	Publisher                  string `json:"publisher,omitempty"`
	Family                     string `json:"family,omitempty"`
	ReleaseDate                string `json:"release_date,omitempty"`
	LastUpdated                string `json:"last_updated,omitempty"`
	KnowledgeCutoff            string `json:"knowledge_cutoff,omitempty"`
	OpenWeights                bool   `json:"open_weights"`
	InputMicrosPerMillion      int64  `json:"input_microusd_per_million,omitempty"`
	OutputMicrosPerMillion     int64  `json:"output_microusd_per_million,omitempty"`
	CachedMicrosPerMillion     int64  `json:"cached_microusd_per_million,omitempty"`
	CacheWriteMicrosPerMillion int64  `json:"cache_write_microusd_per_million,omitempty"`
	PricingKnown               bool   `json:"pricing_known"`
	// PricingSource tells the operator whether a number came from the provider's
	// live catalogue, the reviewed official catalogue, or is intentionally not
	// token-priced (for example a subscription quota).
	PricingSource string `json:"pricing_source,omitempty"`
	// Capability fields are provider-published metadata. They describe the
	// model, not a promise that every capability is implemented by this gateway.
	InputModalities          []string                   `json:"input_modalities,omitempty"`
	OutputModalities         []string                   `json:"output_modalities,omitempty"`
	ContextWindowTokens      int                        `json:"context_window_tokens,omitempty"`
	MaxOutputTokens          int                        `json:"max_output_tokens,omitempty"`
	SupportedParameters      []string                   `json:"supported_parameters,omitempty"`
	CapabilityTags           []string                   `json:"capability_tags,omitempty"`
	SupportsAttachments      bool                       `json:"supports_attachments"`
	SupportsTools            bool                       `json:"supports_tools"`
	SupportsStructuredOutput bool                       `json:"supports_structured_output"`
	SupportsTemperature      bool                       `json:"supports_temperature"`
	PricingTiers             []ProviderModelPricingTier `json:"pricing_tiers,omitempty"`
	GatewayAPI               string                     `json:"gateway_api,omitempty"`
	SupportsReasoning        bool                       `json:"supports_reasoning"`
	// ReasoningEfforts is an allow-list for this exact provider/model pair.
	// An empty list means the gateway must not send a reasoning setting.
	ReasoningEfforts []string               `json:"reasoning_efforts,omitempty"`
	Variants         []ProviderModelVariant `json:"variants,omitempty"`
	APIFamily        string                 `json:"api_family,omitempty"`
	// Availability distinguishes a public reference record from a model the
	// authenticated account confirmed. It is intentionally separate from
	// Supported, which only describes gateway API compatibility.
	Availability string `json:"availability,omitempty"`
	Supported    bool   `json:"supported"`
	Source       string `json:"source"`
}

type inferenceModelLimits struct {
	ContextWindowTokens int
	MaxOutputTokens     int
}

type inferenceModelLimitsResolver func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error)

type modelLimitsCacheEntry struct {
	limits    inferenceModelLimits
	expiresAt time.Time
}

var inferenceModelLimitsCache = struct {
	sync.Mutex
	entries map[string]modelLimitsCacheEntry
}{entries: make(map[string]modelLimitsCacheEntry)}

var inferenceModelLimitsCacheIdentityKey = newInferenceModelLimitsCacheIdentityKey()

func newInferenceModelLimitsCacheIdentityKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	return key
}

func resolveProviderModelLimits(ctx context.Context, provider Provider, model, apiKey string, client *http.Client) (inferenceModelLimits, error) {
	return cachedInferenceModelLimits(ctx, provider, model, apiKey, func(ctx context.Context) (inferenceModelLimits, error) {
		if strings.TrimSpace(apiKey) == "" {
			return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
		}
		lookupCtx, cancel := context.WithTimeout(ctx, modelLimitsResolveTimeout)
		defer cancel()
		metadata, _ := FetchProviderModelMetadata(lookupCtx, client)
		catalogue, err := ListProviderModelsWithMetadata(lookupCtx, provider, apiKey, client, metadata)
		if err != nil {
			return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
		}
		return inferenceModelLimitsFromCatalogue(provider, model, catalogue)
	})
}

// The local worker has no provider key, so it uses only the public metadata
// projection for a preflight. The cloud gateway repeats the check against the
// authenticated provider catalogue before sending the actual provider call.
func resolvePublicProviderModelLimits(ctx context.Context, provider Provider, model, _ string, client *http.Client) (inferenceModelLimits, error) {
	return cachedInferenceModelLimits(ctx, provider, model, "public", func(ctx context.Context) (inferenceModelLimits, error) {
		lookupCtx, cancel := context.WithTimeout(ctx, modelLimitsResolveTimeout)
		defer cancel()
		metadata, err := FetchProviderModelMetadata(lookupCtx, client)
		if err != nil {
			metadata = nil
		}
		if provider == ProviderMiniMax {
			return inferenceModelLimitsFromCatalogue(provider, model, miniMaxModels(metadata))
		}
		if descriptor, ok := metadata.model(provider, model); ok {
			return validateInferenceModelLimits(inferenceModelLimits{ContextWindowTokens: descriptor.ContextWindowTokens, MaxOutputTokens: descriptor.MaxOutputTokens})
		}
		return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
	})
}

func inferenceModelLimitsCacheKey(provider Provider, model, identity string) (string, bool) {
	key := inferenceModelLimitsCacheIdentityKey
	if len(key) == 0 {
		return "", false
	}
	mac := hmac.New(sha256.New, key)
	if _, err := mac.Write([]byte(identity)); err != nil {
		return "", false
	}
	return string(provider) + "\x00" + strings.ToLower(model) + "\x00" + hex.EncodeToString(mac.Sum(nil)), true
}

func cachedInferenceModelLimits(ctx context.Context, provider Provider, model, identity string, resolve func(context.Context) (inferenceModelLimits, error)) (inferenceModelLimits, error) {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	model = strings.TrimSpace(model)
	if ctx == nil || model == "" || len(model) > 200 || resolve == nil {
		return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
	}
	cacheKey, cacheIdentityAvailable := inferenceModelLimitsCacheKey(provider, model, identity)
	now := time.Now().UTC()
	if cacheIdentityAvailable {
		inferenceModelLimitsCache.Lock()
		entry, ok := inferenceModelLimitsCache.entries[cacheKey]
		if ok && now.Before(entry.expiresAt) {
			inferenceModelLimitsCache.Unlock()
			return entry.limits, nil
		}
		delete(inferenceModelLimitsCache.entries, cacheKey)
		inferenceModelLimitsCache.Unlock()
	}

	limits, err := resolve(ctx)
	if err != nil {
		return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
	}
	limits, err = validateInferenceModelLimits(limits)
	if err != nil {
		return inferenceModelLimits{}, err
	}
	if cacheIdentityAvailable {
		inferenceModelLimitsCache.Lock()
		pruneInferenceModelLimitsCache(now)
		if len(inferenceModelLimitsCache.entries) >= maxModelLimitsCacheEntries {
			var oldestKey string
			var oldest time.Time
			for key, cached := range inferenceModelLimitsCache.entries {
				if oldestKey == "" || cached.expiresAt.Before(oldest) {
					oldestKey, oldest = key, cached.expiresAt
				}
			}
			delete(inferenceModelLimitsCache.entries, oldestKey)
		}
		inferenceModelLimitsCache.entries[cacheKey] = modelLimitsCacheEntry{limits: limits, expiresAt: now.Add(modelLimitsCacheTTL)}
		inferenceModelLimitsCache.Unlock()
	}
	return limits, nil
}

func pruneInferenceModelLimitsCache(now time.Time) {
	for key, entry := range inferenceModelLimitsCache.entries {
		if !now.Before(entry.expiresAt) {
			delete(inferenceModelLimitsCache.entries, key)
		}
	}
}

func inferenceModelLimitsFromCatalogue(provider Provider, requestedModel string, catalogue []ProviderModel) (inferenceModelLimits, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	baseModel := requestedModel
	variant := ""
	if provider == ProviderOpenCodeGo {
		baseModel, variant, _ = strings.Cut(requestedModel, "#")
	}
	for _, model := range catalogue {
		if !strings.EqualFold(strings.TrimSpace(model.ID), strings.TrimSpace(baseModel)) || !model.Supported {
			continue
		}
		if variant != "" {
			found := false
			for _, item := range model.Variants {
				if strings.EqualFold(strings.TrimSpace(item.ID), strings.TrimSpace(variant)) {
					found = true
					break
				}
			}
			if !found {
				return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
			}
		}
		maxOutput := model.MaxOutputTokens
		if provider == ProviderMiniMax {
			if known := miniMaxOutputLimit(model.ID); known > 0 && (maxOutput == 0 || known < maxOutput) {
				maxOutput = known
			}
		}
		return validateInferenceModelLimits(inferenceModelLimits{ContextWindowTokens: model.ContextWindowTokens, MaxOutputTokens: maxOutput})
	}
	return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
}

func validateInferenceModelLimits(limits inferenceModelLimits) (inferenceModelLimits, error) {
	if limits.ContextWindowTokens < 1 || limits.MaxOutputTokens < 1 || limits.ContextWindowTokens > 1_000_000_000 || limits.MaxOutputTokens > 1_000_000_000 {
		return inferenceModelLimits{}, fmt.Errorf("provider model limits are unavailable")
	}
	return limits, nil
}

func miniMaxOutputLimit(model string) int {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "minimax-m2"):
		return miniMaxM2CompletionLimit
	case strings.EqualFold(model, "minimax-m3"):
		return miniMaxM3CompletionLimit
	default:
		return 0
	}
}

// ProviderCatalogProfile explains the billing and capability provenance of a
// connector. It is safe to expose to administrators and contains no account
// settings, credentials, or provider response bodies.
type ProviderCatalogProfile struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	BillingModel     string `json:"billing_model"`
	CapabilitySource string `json:"capability_source"`
	PricingSource    string `json:"pricing_source"`
}

func ProviderCatalogProfileFor(provider Provider) ProviderCatalogProfile {
	switch provider {
	case ProviderMiniMax:
		return ProviderCatalogProfile{ID: string(provider), Name: "MiniMax Token Plan", Description: "API directa de MiniMax con facturación por cuota de suscripción. El gateway usa chat de texto; la ficha distingue capacidades declaradas por el proveedor de las capacidades ya implementadas por el gateway.", BillingModel: "subscription_quota", CapabilitySource: "official_catalog", PricingSource: "subscription_quota"}
	case ProviderDeepSeek:
		return ProviderCatalogProfile{ID: string(provider), Name: "DeepSeek Direct", Description: "API directa. La lista de modelos viene del proveedor; las modalidades y effort se enriquecen sólo para modelos documentados.", BillingModel: "token", CapabilitySource: "provider_api_and_official_catalog", PricingSource: "official_catalog_peak"}
	case ProviderOpenRouter:
		return ProviderCatalogProfile{ID: string(provider), Name: "OpenRouter", Description: "Router multi-proveedor. Precio, modalidades, ventana de contexto y parámetros se leen de su catálogo live por modelo.", BillingModel: "token", CapabilitySource: "provider_api", PricingSource: "provider_api"}
	case ProviderOpenAI:
		return ProviderCatalogProfile{ID: string(provider), Name: "OpenAI Direct", Description: "API directa de OpenAI. El endpoint de modelos anuncia disponibilidad; una ficha sólo marca compatible lo que el gateway conoce para su API de chat o Responses.", BillingModel: "token", CapabilitySource: "official_catalog", PricingSource: "official_catalog"}
	case ProviderAnthropic:
		return ProviderCatalogProfile{ID: string(provider), Name: "Anthropic Direct", Description: "API directa de Claude Messages. La disponibilidad viene del proveedor y los precios deben corresponder a la familia exacta del modelo.", BillingModel: "token", CapabilitySource: "provider_api_and_official_catalog", PricingSource: "official_catalog"}
	case ProviderOpenCodeGo:
		return ProviderCatalogProfile{ID: string(provider), Name: "OpenCode Go", Description: "Suscripción Go. El catálogo live publica modelos y variantes; la cuota se mide por ventanas de uso y no equivale a precio por token.", BillingModel: "subscription_quota", CapabilitySource: "provider_api", PricingSource: "subscription_quota"}
	default:
		return ProviderCatalogProfile{ID: string(provider), Name: string(provider), Description: "Proveedor de IA", BillingModel: "unknown", CapabilitySource: "unknown", PricingSource: "unknown"}
	}
}

// ProviderModelVariant is a provider-published named model setting. It is not
// conflated with reasoning effort: a variant can also mean a token budget or
// latency profile.
type ProviderModelVariant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListProviderModels resolves a provider catalogue only in the cloud/local
// backend process. The browser and worker receive the safe projection, never
// the Authorization header or raw provider response.
func ListProviderModels(ctx context.Context, provider Provider, apiKey string, client *http.Client) ([]ProviderModel, error) {
	return listProviderModels(ctx, provider, apiKey, client, nil)
}

// ListProviderModelsWithMetadata combines the provider's authenticated model
// inventory with a public model descriptor snapshot fetched once by the caller.
// This keeps model facts current without sending credentials to the directory.
func ListProviderModelsWithMetadata(ctx context.Context, provider Provider, apiKey string, client *http.Client, metadata *ProviderModelMetadata) ([]ProviderModel, error) {
	return listProviderModels(ctx, provider, apiKey, client, metadata)
}

// PublicProviderModels returns an informational catalog when no account key is
// configured. Entries are intentionally not selectable until the account API
// confirms availability through ListProviderModelsWithMetadata.
func PublicProviderModels(provider Provider, metadata *ProviderModelMetadata) []ProviderModel {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	models := make([]ProviderModel, 0)
	for _, descriptor := range metadata.modelsFor(provider) {
		model := enrichProviderModel(provider, ProviderModel{ID: descriptor.ID, Name: descriptor.Name, Description: descriptor.Description, Source: "models_dev", Availability: "public_reference", Supported: false}, descriptor)
		if provider == ProviderMiniMax {
			model = markMiniMaxSubscriptionQuota(model)
		}
		model.GatewayAPI = "availability_unverified"
		models = append(models, finalizeProviderModel(provider, model))
	}
	sortProviderModels(models)
	return models
}

func listProviderModels(ctx context.Context, provider Provider, apiKey string, client *http.Client, metadata *ProviderModelMetadata) ([]ProviderModel, error) {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	if _, ok := DefaultProviderEndpoint(provider); !ok || strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("provider model catalogue is unavailable")
	}
	switch provider {
	case ProviderMiniMax:
		return listMiniMaxTokenPlanModels(ctx, apiKey, client, metadata, miniMaxTokenPlanRemainsURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	endpoint := ""
	switch provider {
	case ProviderOpenAI:
		endpoint = "https://api.openai.com/v1/models"
	case ProviderDeepSeek:
		endpoint = "https://api.deepseek.com/models"
	case ProviderOpenRouter:
		endpoint = "https://openrouter.ai/api/v1/models"
	case ProviderAnthropic:
		endpoint = "https://api.anthropic.com/v1/models"
	case ProviderOpenCodeGo:
		endpoint = "https://opencode.ai/zen/go/v1/models"
	default:
		return nil, fmt.Errorf("provider model catalogue is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("provider model catalogue is unavailable")
	}
	if provider == ProviderAnthropic {
		req.Header.Set("x-api-key", strings.TrimSpace(apiKey))
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	response, err := client.Do(req)
	if err != nil || response == nil {
		return nil, fmt.Errorf("provider model catalogue is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("provider model catalogue is unavailable")
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
			Pricing     struct {
				Prompt          string `json:"prompt"`
				Completion      string `json:"completion"`
				InputCacheRead  string `json:"input_cache_read"`
				InputCacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
			ContextLength       int      `json:"context_length"`
			MaxOutputTokens     int      `json:"max_output_tokens"`
			MaxCompletionTokens int      `json:"max_completion_tokens"`
			SupportedParameters []string `json:"supported_parameters"`
			Architecture        struct {
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			Variants json.RawMessage `json:"variants"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("provider model catalogue is unavailable")
	}
	models := make([]ProviderModel, 0, min(len(payload.Data), maxProviderModels))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > 200 || len(models) >= maxProviderModels {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.DisplayName)
		}
		if name == "" {
			name = id
		}
		model := ProviderModel{
			ID: id, Name: name, Description: sanitizeModelDescription(item.Description), SupportsReasoning: false, Availability: "account_verified", Supported: true, Source: "provider_api",
			InputModalities:     sanitizeModelCapabilities(item.Architecture.InputModalities),
			OutputModalities:    sanitizeModelCapabilities(item.Architecture.OutputModalities),
			SupportedParameters: sanitizeModelCapabilities(item.SupportedParameters),
			ContextWindowTokens: nonNegative(item.ContextLength),
			MaxOutputTokens:     nonNegative(max(item.MaxOutputTokens, item.MaxCompletionTokens)),
		}
		if descriptor, found := metadata.model(provider, id); found {
			model = enrichProviderModel(provider, model, descriptor)
		}
		if provider == ProviderOpenRouter {
			model.InputMicrosPerMillion = priceMicrosPerMillion(item.Pricing.Prompt)
			model.OutputMicrosPerMillion = priceMicrosPerMillion(item.Pricing.Completion)
			model.CachedMicrosPerMillion = priceMicrosPerMillion(item.Pricing.InputCacheRead)
			model.CacheWriteMicrosPerMillion = priceMicrosPerMillion(item.Pricing.InputCacheWrite)
			model.PricingKnown = strings.TrimSpace(item.Pricing.Prompt) != "" && strings.TrimSpace(item.Pricing.Completion) != ""
			if model.PricingKnown {
				model.PricingSource = "provider_api"
			}
			model.Supported = openRouterTextChatCompatible(item.Architecture.InputModalities, item.Architecture.OutputModalities, item.SupportedParameters)
			model.ReasoningEfforts = openRouterReasoningEfforts(item.SupportedParameters)
			model.SupportsReasoning = len(model.ReasoningEfforts) > 0
		}
		if provider == ProviderDeepSeek {
			model.InputModalities, model.OutputModalities = deepSeekModalities(id)
			// These are published provider capabilities. The gateway currently
			// sends text chat plus reasoning; tool/JSON support is displayed for
			// planning and is not silently enabled as a worker capability.
			model.SupportedParameters = []string{"reasoning_effort", "response_format", "tools"}
			model.ReasoningEfforts = deepSeekReasoningEfforts(id)
			model.SupportsReasoning = len(model.ReasoningEfforts) > 0
			model.Supported = model.SupportsReasoning
		}
		if provider == ProviderOpenAI {
			model.APIFamily, model.Supported = OpenAIModelAPI(id)
			model.GatewayAPI = model.APIFamily
			if len(model.ReasoningEfforts) == 0 {
				model.ReasoningEfforts = openAIReasoningEfforts(id)
			}
			model.SupportsReasoning = len(model.ReasoningEfforts) > 0
			if model.Supported {
				if len(model.InputModalities) == 0 {
					model.InputModalities = []string{"text"}
					if !strings.HasPrefix(strings.ToLower(id), "gpt-3.5") {
						model.InputModalities = append(model.InputModalities, "image")
					}
				}
				if len(model.OutputModalities) == 0 {
					model.OutputModalities = []string{"text"}
				}
				if len(model.SupportedParameters) == 0 {
					model.SupportedParameters = []string{"max_output_tokens"}
				}
			}
		}
		if provider == ProviderOpenCodeGo {
			model.APIFamily, model.Supported = OpenCodeGoModelAPI(id)
			model.Variants = parseProviderModelVariants(item.Variants)
			if !model.Supported {
				model.APIFamily = "unsupported"
			}
			model.PricingSource = "subscription_quota"
		}
		if !model.PricingKnown {
			model = withDefaultCatalogRates(provider, []ProviderModel{model})[0]
		}
		model = finalizeProviderModel(provider, model)
		models = append(models, model)
	}
	sortProviderModels(models)
	return models, nil
}

// listMiniMaxTokenPlanModels verifies that the configured credential belongs
// to MiniMax Token Plan before exposing MiniMax entries as account-verified.
// endpoint is injectable only to keep the HTTP contract testable without
// contacting MiniMax from tests.
func listMiniMaxTokenPlanModels(ctx context.Context, apiKey string, client *http.Client, metadata *ProviderModelMetadata, endpoint string) ([]ProviderModel, error) {
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("MiniMax Token Plan account verification failed")
	}
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("MiniMax Token Plan account verification failed")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	req.Header.Set("Content-Type", "application/json")

	// Do not forward a credential-bearing verification request through redirects.
	safeClient := *client
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := safeClient.Do(req)
	if err != nil || response == nil || response.Body == nil {
		return nil, fmt.Errorf("MiniMax Token Plan account verification failed")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || !validMiniMaxTokenPlanRemains(response.Body) {
		return nil, fmt.Errorf("MiniMax Token Plan account verification failed")
	}

	models := miniMaxModels(metadata)
	for index := range models {
		models[index] = finalizeProviderModel(ProviderMiniMax, models[index])
	}
	sortProviderModels(models)
	return models, nil
}

func validMiniMaxTokenPlanRemains(body io.Reader) bool {
	const maxResponseBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxResponseBytes {
		return false
	}
	var payload struct {
		ModelRemains json.RawMessage `json:"model_remains"`
		BaseResp     *struct {
			StatusCode *int `json:"status_code"`
		} `json:"base_resp"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&payload); err != nil || payload.BaseResp == nil || payload.BaseResp.StatusCode == nil || *payload.BaseResp.StatusCode != 0 || len(payload.ModelRemains) == 0 {
		return false
	}
	var remains []struct {
		ModelName string `json:"model_name"`
	}
	if err := json.Unmarshal(payload.ModelRemains, &remains); err != nil {
		return false
	}
	for _, model := range remains {
		if strings.TrimSpace(model.ModelName) == "" {
			return false
		}
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func miniMaxModels(metadata *ProviderModelMetadata) []ProviderModel {
	models := make([]ProviderModel, 0)
	for _, descriptor := range metadata.modelsFor(ProviderMiniMax) {
		model := enrichProviderModel(ProviderMiniMax, ProviderModel{ID: descriptor.ID, Name: descriptor.Name, Description: descriptor.Description, Supported: true, Source: "models_dev"}, descriptor)
		if strings.EqualFold(strings.TrimSpace(model.ID), "MiniMax-M3") && (model.ContextWindowTokens == 0 || model.ContextWindowTokens > 1000000) {
			model.ContextWindowTokens = 1000000
		}
		if known := miniMaxOutputLimit(model.ID); known > 0 && (model.MaxOutputTokens == 0 || known < model.MaxOutputTokens) {
			model.MaxOutputTokens = known
		}
		// The local MiniMax adapter sends Chat Completions. Do not make media-only
		// entries executable merely because the public catalog describes them.
		model.Supported = containsModelCapability(model.InputModalities, "text") && containsModelCapability(model.OutputModalities, "text")
		model = markMiniMaxSubscriptionQuota(model)
		models = append(models, model)
	}
	if len(models) > 0 {
		return models
	}
	models = []ProviderModel{
		{ID: "MiniMax-M3", Name: "MiniMax M3", Description: "Modelo principal de MiniMax para texto y código; el plan del proveedor anuncia comprensión multimodal.", InputModalities: []string{"text", "image", "video"}, OutputModalities: []string{"text"}, ContextWindowTokens: 1000000, SupportsReasoning: true, Supported: true, Source: "official_catalog"},
		{ID: "MiniMax-M2.7", Name: "MiniMax M2.7", Description: "Modelo de MiniMax para entrada multimodal, disponible según el Token Plan.", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}, Supported: true, Source: "official_catalog"},
		{ID: "MiniMax-M2.7-highspeed", Name: "MiniMax M2.7 Highspeed", Description: "Variante de menor latencia de MiniMax M2.7, disponible según el Token Plan.", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}, Supported: true, Source: "official_catalog"},
	}
	for index := range models {
		if known := miniMaxOutputLimit(models[index].ID); known > 0 && (models[index].MaxOutputTokens == 0 || known < models[index].MaxOutputTokens) {
			models[index].MaxOutputTokens = known
		}
		models[index] = markMiniMaxSubscriptionQuota(models[index])
	}
	return models
}

func markMiniMaxSubscriptionQuota(model ProviderModel) ProviderModel {
	model.InputMicrosPerMillion = 0
	model.OutputMicrosPerMillion = 0
	model.CachedMicrosPerMillion = 0
	model.CacheWriteMicrosPerMillion = 0
	model.PricingKnown = false
	model.PricingSource = "subscription_quota"
	model.PricingTiers = nil
	return model
}

func enrichProviderModel(provider Provider, model ProviderModel, descriptor providerModelMetadata) ProviderModel {
	if model.Name == "" || model.Name == model.ID {
		model.Name = descriptor.Name
	}
	if model.Description == "" {
		model.Description = descriptor.Description
	}
	model.Publisher = ProviderCatalogProfileFor(provider).Name
	if provider == ProviderOpenRouter {
		if publisher, _, found := strings.Cut(strings.TrimSpace(descriptor.ID), "/"); found && publisher != "" {
			model.Publisher = publisher
		}
	}
	model.Family = descriptor.Family
	model.ReleaseDate = descriptor.ReleaseDate
	model.LastUpdated = descriptor.LastUpdated
	model.KnowledgeCutoff = descriptor.KnowledgeCutoff
	model.OpenWeights = descriptor.OpenWeights
	if len(descriptor.InputModalities) > 0 {
		model.InputModalities = descriptor.InputModalities
	}
	if len(descriptor.OutputModalities) > 0 {
		model.OutputModalities = descriptor.OutputModalities
	}
	if descriptor.ContextWindowTokens > 0 && (model.ContextWindowTokens == 0 || descriptor.ContextWindowTokens < model.ContextWindowTokens) {
		// The strictest positive source wins: stale public metadata must not
		// widen a live provider limit, while a conservative descriptor can still
		// protect requests if the provider has recently lowered its limit.
		model.ContextWindowTokens = descriptor.ContextWindowTokens
	}
	if descriptor.MaxOutputTokens > 0 && (model.MaxOutputTokens == 0 || descriptor.MaxOutputTokens < model.MaxOutputTokens) {
		model.MaxOutputTokens = descriptor.MaxOutputTokens
	}
	model.SupportsAttachments = descriptor.SupportsAttachments
	model.SupportsTools = descriptor.SupportsTools || model.SupportsTools
	model.SupportsStructuredOutput = descriptor.SupportsStructuredOutput || model.SupportsStructuredOutput
	model.SupportsTemperature = descriptor.SupportsTemperature
	model.SupportsReasoning = descriptor.SupportsReasoning || model.SupportsReasoning
	if len(descriptor.ReasoningEfforts) > 0 {
		model.ReasoningEfforts = descriptor.ReasoningEfforts
	}
	model.PricingTiers = descriptor.PricingTiers
	if !model.PricingKnown && descriptor.Rates.Known {
		model.InputMicrosPerMillion = descriptor.Rates.Input
		model.OutputMicrosPerMillion = descriptor.Rates.Output
		model.CachedMicrosPerMillion = descriptor.Rates.CacheRead
		model.CacheWriteMicrosPerMillion = descriptor.Rates.CacheWrite
		model.PricingKnown = true
		model.PricingSource = "models_dev_catalog"
	}
	return model
}

func sortProviderModels(models []ProviderModel) {
	sort.Slice(models, func(i, j int) bool {
		if models[i].PricingKnown != models[j].PricingKnown {
			return models[i].PricingKnown
		}
		if models[i].InputMicrosPerMillion != models[j].InputMicrosPerMillion {
			return models[i].InputMicrosPerMillion < models[j].InputMicrosPerMillion
		}
		return models[i].ID < models[j].ID
	})
}

func sanitizeModelDescription(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 600 || strings.ContainsAny(value, "\x00") {
		return ""
	}
	return value
}

func finalizeProviderModel(provider Provider, model ProviderModel) ProviderModel {
	if model.Description == "" {
		model.Description = "Modelo publicado por " + ProviderCatalogProfileFor(provider).Name + "."
	}
	if model.GatewayAPI == "" && model.Supported {
		switch provider {
		case ProviderAnthropic:
			model.GatewayAPI = "messages"
		case ProviderOpenCodeGo:
			model.GatewayAPI = model.APIFamily
		default:
			model.GatewayAPI = "chat_completions"
		}
	}
	if model.Availability == "" {
		if model.Supported {
			model.Availability = "account_verified"
		} else {
			model.Availability = "gateway_incompatible"
		}
	}
	if !model.Supported && model.Availability == "account_verified" {
		model.Availability = "gateway_incompatible"
	}
	tags := append([]string{}, model.InputModalities...)
	if model.SupportsAttachments {
		tags = append(tags, "attachments")
	}
	if model.OpenWeights {
		tags = append(tags, "open_weights")
	}
	if model.SupportsTools || containsModelCapability(model.SupportedParameters, "tools") || containsModelCapability(model.SupportedParameters, "functions") || containsModelCapability(model.SupportedParameters, "tool_choice") {
		tags = append(tags, "tools", "agent")
	}
	if model.SupportsStructuredOutput || containsModelCapability(model.SupportedParameters, "response_format") || containsModelCapability(model.SupportedParameters, "structured_outputs") {
		tags = append(tags, "structured_output")
	}
	if model.SupportsTemperature {
		tags = append(tags, "temperature")
	}
	if model.SupportsReasoning {
		tags = append(tags, "reasoning")
	}
	model.CapabilityTags = sanitizeModelCapabilities(tags)
	return model
}

func deepSeekModalities(id string) ([]string, []string) {
	if strings.EqualFold(strings.TrimSpace(id), "deepseek-flash") {
		return []string{"text", "image"}, []string{"text"}
	}
	return []string{"text"}, []string{"text"}
}

func sanitizeModelCapabilities(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, min(len(values), 64))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || len(value) > 64 || strings.ContainsAny(value, "\t\r\n") || len(result) >= 64 {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func parseProviderModelVariants(raw json.RawMessage) []ProviderModelVariant {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var objects []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &objects); err == nil {
		return sanitizeProviderModelVariants(objects)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	objects = make([]struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}, 0, len(ids))
	for _, id := range ids {
		objects = append(objects, struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}{ID: id, Name: id})
	}
	return sanitizeProviderModelVariants(objects)
}

func sanitizeProviderModelVariants(values []struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}) []ProviderModelVariant {
	result := make([]ProviderModelVariant, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value.ID)
		if id == "" || len(id) > 80 || strings.ContainsAny(id, "#\t\r\n") {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(value.Name)
		if name == "" || len(name) > 160 {
			name = id
		}
		result = append(result, ProviderModelVariant{ID: id, Name: name})
	}
	return result
}

func deepSeekReasoningEfforts(id string) []string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "deepseek-flash", "deepseek-v4-pro":
		return []string{"low", "high", "max"}
	default:
		return nil
	}
}

func openAIReasoningEfforts(id string) []string {
	if api, supported := OpenAIModelAPI(id); supported && api == openCodeGoResponsesAPI {
		return []string{"none", "minimal", "low", "medium", "high", "xhigh"}
	}
	return nil
}

func openRouterReasoningEfforts(parameters []string) []string {
	if !containsModelCapability(parameters, "reasoning") && !containsModelCapability(parameters, "reasoning_effort") {
		return nil
	}
	// OpenRouter documents these request-level values. The model endpoint's
	// supported_parameters signal is the per-model guard; do not expose this
	// control for a row that does not advertise either reasoning field.
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
}

// IsAllowedReasoningEffort applies the common shape guard used before a saved
// policy reaches a provider adapter. Exact per-model validation is performed
// against ProviderModel.ReasoningEfforts while the policy is saved.
func IsAllowedReasoningEffort(effort string) bool {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

// OpenRouter exposes its entire cross-provider catalogue. This gateway sends
// text messages through /chat/completions, so hide models that explicitly lack
// text input/output or a completion-token control. Older/incomplete catalogue
// rows remain selectable rather than being incorrectly treated as incompatible.
func openRouterTextChatCompatible(input, output, parameters []string) bool {
	if len(input) == 0 && len(output) == 0 && len(parameters) == 0 {
		return true
	}
	if !containsModelCapability(input, "text") || !containsModelCapability(output, "text") {
		return false
	}
	if len(parameters) == 0 {
		return true
	}
	return containsModelCapability(parameters, "max_completion_tokens") || containsModelCapability(parameters, "max_tokens")
}

func containsModelCapability(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}

func withDefaultCatalogRates(provider Provider, models []ProviderModel) []ProviderModel {
	for index := range models {
		rates, priced, err := automationcost.RatesFor(string(provider), models[index].ID, "")
		if err != nil || !priced {
			continue
		}
		models[index].InputMicrosPerMillion = rates.InputMicrosPerMillion
		models[index].OutputMicrosPerMillion = rates.OutputMicrosPerMillion
		models[index].CachedMicrosPerMillion = rates.CachedMicrosPerMillion
		models[index].CacheWriteMicrosPerMillion = rates.CacheWriteMicrosPerMillion
		models[index].PricingKnown = true
		models[index].PricingSource = "official_catalog"
	}
	return models
}

func priceMicrosPerMillion(raw string) int64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(math.MaxInt64)/1e12 {
		return 0
	}
	return int64(math.Round(value * 1e12))
}
