package automationagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type providerModelsRoundTripper func(*http.Request) (*http.Response, error)

func TestInferenceModelLimitsCacheKeyUsesKeyedIdentityFingerprint(t *testing.T) {
	identity := "provider-credential-fixture-value"
	first, ok := inferenceModelLimitsCacheKey(ProviderOpenAI, "gpt-test", identity)
	if !ok {
		t.Skip("process-local cache key unavailable")
	}
	if strings.Contains(first, identity) {
		t.Fatal("provider credential appeared in the model-limits cache key")
	}
	second, ok := inferenceModelLimitsCacheKey(ProviderOpenAI, "gpt-test", identity)
	if !ok || second != first {
		t.Fatal("same provider identity did not produce a stable process-local cache key")
	}
	other, ok := inferenceModelLimitsCacheKey(ProviderOpenAI, "gpt-test", "another-provider-credential")
	if !ok || other == first {
		t.Fatal("different provider identities shared a model-limits cache key")
	}
}

func (fn providerModelsRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestListOpenCodeGoModelsUsesLiveCatalogueAndMarksAPICompatibility(t *testing.T) {
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://opencode.ai/zen/go/v1/models" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected catalogue request: %s %#v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"glm-5.3-flash","variants":[{"id":"high","name":"High"}]},{"id":"gpt-6-luna"},{"id":"minimax-m3"},{"id":"omen-alpha"}]}`))}, nil
	})}
	models, err := ListProviderModels(context.Background(), ProviderOpenCodeGo, "test-key", client)
	if err != nil || len(models) != 4 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	if !models[0].Supported || models[0].APIFamily != openCodeGoChatAPI || !models[1].Supported || models[1].APIFamily != openCodeGoResponsesAPI || !models[2].Supported || models[2].APIFamily != openCodeGoMessagesAPI || models[3].Supported || models[3].APIFamily != "unsupported" {
		t.Fatalf("unexpected OpenCode Go compatibility metadata: %#v", models)
	}
	if len(models[0].Variants) != 1 || models[0].Variants[0].ID != "high" {
		t.Fatalf("OpenCode Go variants were not projected: %#v", models[0])
	}
}

func TestListOpenRouterModelsProjectsEveryTokenPriceDimension(t *testing.T) {
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://openrouter.ai/api/v1/models" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected catalogue request: %s %#v", request.URL, request.Header)
		}
		// OpenRouter returns USD per token; these values correspond to
		// $0.25/$1.50/$0.025/$0.3125 per one million tokens.
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"vendor/model","name":"Model","pricing":{"prompt":"0.00000025","completion":"0.0000015","input_cache_read":"0.000000025","input_cache_write":"0.0000003125"},"context_length":1000000,"max_output_tokens":32000,"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"supported_parameters":["max_completion_tokens","reasoning_effort"]}]}`))}, nil
	})}
	models, err := ListProviderModels(context.Background(), ProviderOpenRouter, "test-key", client)
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	model := models[0]
	if !model.PricingKnown || model.InputMicrosPerMillion != 250000 || model.OutputMicrosPerMillion != 1500000 || model.CachedMicrosPerMillion != 25000 || model.CacheWriteMicrosPerMillion != 312500 {
		t.Fatalf("unexpected model pricing projection: %#v", model)
	}
	if model.ContextWindowTokens != 1000000 || model.MaxOutputTokens != 32000 || !reflect.DeepEqual(model.InputModalities, []string{"image", "text"}) || !reflect.DeepEqual(model.SupportedParameters, []string{"max_completion_tokens", "reasoning_effort"}) {
		t.Fatalf("unexpected model capability projection: %#v", model)
	}
}

func TestListOpenRouterModelsDisablesExplicitlyNonTextOrNonChatModels(t *testing.T) {
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[
			{"id":"text-chat","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"supported_parameters":["max_completion_tokens","reasoning_effort"]},
			{"id":"image-out","architecture":{"input_modalities":["text"],"output_modalities":["image"]},"supported_parameters":["max_completion_tokens"]},
			{"id":"no-token-limit","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"supported_parameters":["temperature"]}
		]}`))}, nil
	})}
	models, err := ListProviderModels(context.Background(), ProviderOpenRouter, "test-key", client)
	if err != nil || len(models) != 3 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	byID := make(map[string]ProviderModel, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	if !byID["text-chat"].Supported || byID["image-out"].Supported || byID["no-token-limit"].Supported || !reflect.DeepEqual(byID["text-chat"].ReasoningEfforts, []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("unexpected OpenRouter text chat compatibility: %#v", byID)
	}
}

func TestListDeepSeekModelsUsesLiveCatalogueAndAllowsCurrentChatModels(t *testing.T) {
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.deepseek.com/models" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected DeepSeek catalogue request: %s %#v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"deepseek-flash"},{"id":"deepseek-v4-pro"},{"id":"deepseek-vision-preview"}]}`))}, nil
	})}
	models, err := ListProviderModels(context.Background(), ProviderDeepSeek, "test-key", client)
	if err != nil || len(models) != 3 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	byID := make(map[string]ProviderModel, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	if !byID["deepseek-flash"].Supported || !byID["deepseek-v4-pro"].Supported || byID["deepseek-vision-preview"].Supported || !byID["deepseek-flash"].PricingKnown || !reflect.DeepEqual(byID["deepseek-flash"].ReasoningEfforts, []string{"low", "high", "max"}) {
		t.Fatalf("unexpected DeepSeek model projection: %#v", byID)
	}
	if !reflect.DeepEqual(byID["deepseek-flash"].InputModalities, []string{"text", "image"}) || !reflect.DeepEqual(byID["deepseek-v4-pro"].InputModalities, []string{"text"}) {
		t.Fatalf("unexpected DeepSeek modality projection: %#v", byID)
	}
}

func TestListAnthropicModelsUsesProviderKeyHeader(t *testing.T) {
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.anthropic.com/v1/models" || request.Header.Get("x-api-key") != "test-key" || request.Header.Get("anthropic-version") != "2023-06-01" || request.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Anthropic catalogue request: %s %#v", request.URL, request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"claude-sonnet-example","display_name":"Claude Sonnet"}]}`))}, nil
	})}
	models, err := ListProviderModels(context.Background(), ProviderAnthropic, "test-key", client)
	if err != nil || len(models) != 1 || models[0].Name != "Claude Sonnet" || models[0].PricingKnown {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestListOpenAIModelsMarksOnlyKnownTextAPIsCompatibleAndProjectsEffort(t *testing.T) {
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-6-sol"},{"id":"gpt-4.1-mini"},{"id":"text-embedding-3-small"}]}`))}, nil
	})}
	models, err := ListProviderModels(context.Background(), ProviderOpenAI, "test-key", client)
	if err != nil || len(models) != 3 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	byID := make(map[string]ProviderModel, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	if !byID["gpt-6-sol"].Supported || byID["gpt-6-sol"].GatewayAPI != "responses" || !reflect.DeepEqual(byID["gpt-6-sol"].ReasoningEfforts, []string{"none", "minimal", "low", "medium", "high", "xhigh"}) || !byID["gpt-4.1-mini"].Supported || byID["text-embedding-3-small"].Supported {
		t.Fatalf("unexpected OpenAI model compatibility projection: %#v", byID)
	}
}

func TestModelsDevMetadataEnrichesLiveAvailabilityAndKeepsPriceTiers(t *testing.T) {
	document := `{"openai":{"models":{"gpt-6-sol":{"id":"gpt-6-sol","name":"GPT-6 Sol","description":"Agent model","family":"gpt","attachment":true,"reasoning":true,"reasoning_options":[{"type":"effort","values":["low","high"]}],"tool_call":true,"structured_output":true,"temperature":true,"knowledge":"2026-01-01","release_date":"2026-02-01","last_updated":"2026-02-02","modalities":{"input":["text","image","pdf"],"output":["text"]},"limit":{"context":1000000,"output":128000},"cost":{"input":2,"output":10,"cache_read":0.2,"cache_write":2.5,"tiers":[{"input":4,"output":20,"cache_read":0.4,"tier":{"type":"context","size":200000}}]}}}}}`
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != modelsDevCatalogURL {
			t.Fatalf("unexpected metadata request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(document))}, nil
	})}
	metadata, err := FetchProviderModelMetadata(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	liveClient := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-6-sol"}]}`))}, nil
	})}
	models, err := ListProviderModelsWithMetadata(context.Background(), ProviderOpenAI, "test-key", liveClient, metadata)
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	model := models[0]
	if !model.PricingKnown || model.InputMicrosPerMillion != 2_000_000 || model.OutputMicrosPerMillion != 10_000_000 || model.CacheWriteMicrosPerMillion != 2_500_000 || model.ContextWindowTokens != 1_000_000 || !model.SupportsTools || !model.SupportsStructuredOutput || !reflect.DeepEqual(model.ReasoningEfforts, []string{"high", "low"}) || len(model.PricingTiers) != 1 || model.PricingTiers[0].ThresholdTokens != 200000 {
		t.Fatalf("unexpected enriched model: %#v", model)
	}
}

func TestResolveProviderModelLimitsForOpenAIDeepSeekOpenRouterAndMiniMax(t *testing.T) {
	const publicMetadata = `{"openai":{"models":{"gpt-4.1-mini":{"id":"gpt-4.1-mini","modalities":{"input":["text"],"output":["text"]},"limit":{"context":4096,"output":2048}}}},"deepseek":{"models":{"deepseek-flash":{"id":"deepseek-flash","modalities":{"input":["text"],"output":["text"]},"limit":{"context":2048,"output":1024}}}},"openrouter":{"models":{"vendor/model-limits":{"id":"vendor/model-limits","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000,"output":400}}}},"minimax":{"models":{"MiniMax-M3":{"id":"MiniMax-M3","modalities":{"input":["text"],"output":["text"]},"limit":{"context":1000000,"output":131072}}}}}`
	var requests atomic.Int64
	client := &http.Client{Transport: providerModelsRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.String() == modelsDevCatalogURL {
			if request.Header.Get("Authorization") != "" {
				t.Fatal("provider credential must never be sent to public model metadata")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(publicMetadata))}, nil
		}
		var body string
		switch request.URL.String() {
		case "https://api.openai.com/v1/models":
			if request.Header.Get("Authorization") != "Bearer unique-openai-limit-key" {
				t.Fatal("OpenAI account model request did not use its provider key")
			}
			body = `{"data":[{"id":"gpt-4.1-mini"}]}`
		case "https://api.deepseek.com/models":
			if request.Header.Get("Authorization") != "Bearer unique-deepseek-limit-key" {
				t.Fatal("DeepSeek account model request did not use its provider key")
			}
			body = `{"data":[{"id":"deepseek-flash"}]}`
		case "https://openrouter.ai/api/v1/models":
			if request.Header.Get("Authorization") != "Bearer unique-openrouter-limit-key" {
				t.Fatal("OpenRouter account model request did not use its provider key")
			}
			body = `{"data":[{"id":"vendor/model-limits","context_length":800,"max_output_tokens":300,"architecture":{"input_modalities":["text"],"output_modalities":["text"]},"supported_parameters":["max_completion_tokens"]}]}`
		case miniMaxTokenPlanRemainsURL:
			if request.Header.Get("Authorization") != "Bearer unique-minimax-limit-key" {
				t.Fatal("MiniMax token-plan check did not use its provider key")
			}
			body = `{"model_remains":[{"model_name":"MiniMax-M3"}],"base_resp":{"status_code":0}}`
		default:
			t.Fatalf("unexpected model limit lookup URL: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	tests := []struct {
		provider Provider
		model    string
		apiKey   string
		want     inferenceModelLimits
	}{
		{ProviderOpenAI, "gpt-4.1-mini", "unique-openai-limit-key", inferenceModelLimits{ContextWindowTokens: 4096, MaxOutputTokens: 2048}},
		{ProviderDeepSeek, "deepseek-flash", "unique-deepseek-limit-key", inferenceModelLimits{ContextWindowTokens: 2048, MaxOutputTokens: 1024}},
		// A live provider limit lower than the public descriptor is retained.
		{ProviderOpenRouter, "vendor/model-limits", "unique-openrouter-limit-key", inferenceModelLimits{ContextWindowTokens: 800, MaxOutputTokens: 300}},
		// MiniMax's adapter cap is stricter than the published output value.
		{ProviderMiniMax, "MiniMax-M3", "unique-minimax-limit-key", inferenceModelLimits{ContextWindowTokens: 1000000, MaxOutputTokens: miniMaxM3CompletionLimit}},
	}
	// Each -count iteration needs cold fixture identities to prove both the
	// first lookup and reuse. Remove only this test's keys, preserving unrelated
	// account entries and the production TTL/identity behaviour.
	resetFixtureEntries := func() {
		inferenceModelLimitsCache.Lock()
		defer inferenceModelLimitsCache.Unlock()
		for _, test := range tests {
			if key, available := inferenceModelLimitsCacheKey(test.provider, test.model, test.apiKey); available {
				delete(inferenceModelLimitsCache.entries, key)
			}
		}
	}
	resetFixtureEntries()
	t.Cleanup(resetFixtureEntries)
	for _, test := range tests {
		t.Run(string(test.provider), func(t *testing.T) {
			limits, err := resolveProviderModelLimits(context.Background(), test.provider, test.model, test.apiKey, client)
			if err != nil || limits != test.want {
				t.Fatalf("resolved model limits = %#v / %v, want %#v", limits, err, test.want)
			}
			cached, err := resolveProviderModelLimits(context.Background(), test.provider, test.model, test.apiKey, client)
			if err != nil || cached != test.want {
				t.Fatalf("cached model limits = %#v / %v, want %#v", cached, err, test.want)
			}
		})
	}
	if requests.Load() != 8 {
		t.Fatalf("model metadata/provider catalog lookups were not cached per account and model: %d requests", requests.Load())
	}
}

func TestInferenceModelLimitsFailClosedWhenMetadataIsMissingOrModelIsUnselectable(t *testing.T) {
	for _, test := range []struct {
		name      string
		provider  Provider
		model     string
		catalogue []ProviderModel
	}{
		{name: "missing context", provider: ProviderOpenAI, model: "gpt-test", catalogue: []ProviderModel{{ID: "gpt-test", MaxOutputTokens: 100, Supported: true}}},
		{name: "missing output", provider: ProviderDeepSeek, model: "deepseek-test", catalogue: []ProviderModel{{ID: "deepseek-test", ContextWindowTokens: 1000, Supported: true}}},
		{name: "unsupported model", provider: ProviderOpenRouter, model: "vendor/unsupported", catalogue: []ProviderModel{{ID: "vendor/unsupported", ContextWindowTokens: 1000, MaxOutputTokens: 100, Supported: false}}},
		{name: "wrong model id", provider: ProviderMiniMax, model: "MiniMax-M3", catalogue: []ProviderModel{{ID: "MiniMax-M2.7", ContextWindowTokens: 1000000, MaxOutputTokens: 1000, Supported: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if limits, err := inferenceModelLimitsFromCatalogue(test.provider, test.model, test.catalogue); err == nil || limits != (inferenceModelLimits{}) {
				t.Fatalf("incomplete or mismatched model metadata was accepted: %#v / %v", limits, err)
			}
		})
	}
}

func TestPublicProviderModelsAreInformationalUntilAccountAvailabilityIsVerified(t *testing.T) {
	metadata := &ProviderModelMetadata{models: map[Provider]map[string]providerModelMetadata{
		ProviderMiniMax: {"minimax-m3": {ID: "MiniMax-M3", Name: "MiniMax M3", InputModalities: []string{"text"}, OutputModalities: []string{"text"}, Rates: providerModelRates{Input: 300000, Output: 1200000, Known: true}}},
	}}
	models := PublicProviderModels(ProviderMiniMax, metadata)
	if len(models) != 1 || models[0].Supported || models[0].GatewayAPI != "availability_unverified" || models[0].PricingKnown || models[0].PricingSource != "subscription_quota" || models[0].InputMicrosPerMillion != 0 || models[0].OutputMicrosPerMillion != 0 {
		t.Fatalf("unexpected public model catalog: %#v", models)
	}
}

func TestMiniMaxTokenPlanVerificationUsesRemainsEndpointAndHidesPayAsYouGoPrices(t *testing.T) {
	const apiKey = "test-minimax-token-plan-key"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/token_plan/remains" || request.Header.Get("Authorization") != "Bearer "+apiKey || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected MiniMax Token Plan verification request: method=%q path=%q", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model_remains":[{"model_name":"MiniMax-M3","current_interval_remaining_count":100}],"base_resp":{"status_code":0,"status_msg":"success"}}`)
	}))
	defer server.Close()

	metadata := &ProviderModelMetadata{models: map[Provider]map[string]providerModelMetadata{
		ProviderMiniMax: {"minimax-m3": {ID: "MiniMax-M3", Name: "MiniMax M3", InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}, ReasoningEfforts: []string{"low", "high"}, SupportsReasoning: true, Rates: providerModelRates{Input: 300000, Output: 1200000, CacheRead: 100000, CacheWrite: 400000, Known: true}, PricingTiers: []ProviderModelPricingTier{{ThresholdTokens: 100000, InputMicrosPerMillion: 500000}}}},
	}}
	models, err := listMiniMaxTokenPlanModels(context.Background(), apiKey, server.Client(), metadata, server.URL+"/v1/token_plan/remains")
	if err != nil || len(models) != 1 {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	model := models[0]
	if !model.Supported || model.Availability != "account_verified" || model.GatewayAPI != "chat_completions" || !reflect.DeepEqual(model.InputModalities, []string{"text", "image"}) || !reflect.DeepEqual(model.ReasoningEfforts, []string{"low", "high"}) {
		t.Fatalf("MiniMax capabilities or verified availability were not preserved: %#v", model)
	}
	if model.PricingKnown || model.PricingSource != "subscription_quota" || model.InputMicrosPerMillion != 0 || model.OutputMicrosPerMillion != 0 || model.CachedMicrosPerMillion != 0 || model.CacheWriteMicrosPerMillion != 0 || len(model.PricingTiers) != 0 {
		t.Fatalf("Token Plan model exposed pay-as-you-go prices: %#v", model)
	}
	profile := ProviderCatalogProfileFor(ProviderMiniMax)
	if profile.BillingModel != "subscription_quota" || profile.PricingSource != "subscription_quota" {
		t.Fatalf("unexpected MiniMax billing profile: %#v", profile)
	}
}

func TestMiniMaxPolicyValidationCatalogueWithoutMetadataPreservesM3BinaryReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("catalogue verification must not perform inference: %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, _ = io.WriteString(writer, `{"model_remains":[{"model_name":"MiniMax-M3"}],"base_resp":{"status_code":0}}`)
	}))
	defer server.Close()
	models, err := listMiniMaxTokenPlanModels(context.Background(), "test-key", server.Client(), nil, server.URL)
	if err != nil || len(models) == 0 {
		t.Fatalf("verified catalogue unavailable: %v", err)
	}
	foundM3 := false
	for _, model := range models {
		if model.ID == "MiniMax-M3" {
			foundM3 = true
			if !model.SupportsReasoning || len(model.ReasoningEfforts) != 0 || !model.Supported || model.Availability != "account_verified" {
				t.Fatalf("M3 binary reasoning missing from policy-save catalogue: %#v", model)
			}
		} else if model.SupportsReasoning {
			t.Fatalf("binary reasoning was extended to another model: %s", model.ID)
		}
		if model.PricingKnown || model.PricingSource != "subscription_quota" {
			t.Fatalf("subscription quota misrepresented as token pricing: %#v", model)
		}
	}
	if !foundM3 {
		t.Fatal("M3 absent from verified catalogue")
	}
}

func TestMiniMaxTokenPlanVerificationFailsClosedWithoutLeakingSecrets(t *testing.T) {
	const apiKey = "test-minimax-secret-key"
	const sensitiveBody = `{"error":"provider echoed test-minimax-secret-key"}`
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: sensitiveBody},
		{name: "forbidden", status: http.StatusForbidden, body: sensitiveBody},
		{name: "provider error", status: http.StatusBadGateway, body: sensitiveBody},
		{name: "invalid JSON", status: http.StatusOK, body: `{`},
		{name: "missing quota response", status: http.StatusOK, body: `{"base_resp":{"status_code":0}}`},
		{name: "application error", status: http.StatusOK, body: `{"model_remains":[],"base_resp":{"status_code":1004,"status_msg":"test-minimax-secret-key"}}`},
		{name: "invalid quota shape", status: http.StatusOK, body: `{"model_remains":[{}],"base_resp":{"status_code":0}}`},
		{name: "trailing data", status: http.StatusOK, body: `{"model_remains":[],"base_resp":{"status_code":0}} trailing`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			models, err := listMiniMaxTokenPlanModels(context.Background(), apiKey, server.Client(), nil, server.URL)
			if err == nil || models != nil {
				t.Fatalf("expected fail-closed response, models=%#v err=%v", models, err)
			}
			if strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), "provider echoed") || strings.Contains(err.Error(), "1004") {
				t.Fatalf("verification error leaked provider/key data: %v", err)
			}
		})
	}
}
