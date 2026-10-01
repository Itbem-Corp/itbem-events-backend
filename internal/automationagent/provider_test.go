package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newProviderTestClient(config ProviderConfig, client *http.Client) *httpProviderClient {
	provider := NewProviderClient(config, client).(*httpProviderClient)
	provider.resolveLimits = func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error) {
		return inferenceModelLimits{ContextWindowTokens: 1_000_000, MaxOutputTokens: MaxCompletionTokens}, nil
	}
	return provider
}

func TestDeepSeekNativeOutputLimitReachesHTTPWithAndWithoutThinking(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, limit := range []int{4096, 32768} {
			t.Run(fmt.Sprintf("thinking=%t/limit=%d", enabled, limit), func(t *testing.T) {
				var calls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						http.Error(w, "invalid JSON", http.StatusBadRequest)
						return
					}
					// Emulate the documented API rather than accepting the OpenAI alias.
					if body["max_tokens"] != float64(limit) || body["max_completion_tokens"] != nil || body["max_output_tokens"] != nil {
						http.Error(w, "native output limit required", http.StatusBadRequest)
						return
					}
					thinking := "disabled"
					if enabled {
						thinking = "enabled"
					}
					if mapValue(body["thinking"])["type"] != thinking {
						t.Error("output limit must preserve the frozen thinking setting")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "bounded-response", "model": "deepseek-flash", "usage": map[string]any{"completion_tokens": limit}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "synthetic answer"}}}})
				}))
				defer server.Close()
				client := newProviderTestClient(ProviderConfig{Provider: ProviderDeepSeek, Model: "deepseek-flash", Endpoint: server.URL, ReasoningEnabled: enabled, ReasoningEffort: "high", secret: "test-key"}, server.Client())
				completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "synthetic case"}}, limit)
				if err != nil || completion.Content != "synthetic answer" || calls.Load() != 1 {
					t.Fatalf("native bounded request failed: completion=%q calls=%d error=%v", completion.Content, calls.Load(), err)
				}
			})
		}
	}
}

func TestProviderConfigDefaultsToMiniMaxM3AndRejectsUnsafeEndpoints(t *testing.T) {
	config, err := LoadProviderConfig(func(name string) string {
		if name == "MINIMAX_API_KEY" {
			return "test-key"
		}
		return ""
	})
	if err != nil || config.Provider != ProviderMiniMax || config.Model != "MiniMax-M3" {
		t.Fatalf("unexpected config: %#v, %v", config, err)
	}
	_, err = LoadProviderConfig(func(name string) string {
		if name == "OPENAI_API_KEY" {
			return "test-key"
		}
		if name == "ITBEM_AI_PROVIDER" {
			return "openai"
		}
		if name == "OPENAI_API_BASE_URL" {
			return "http://remote.invalid"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected insecure endpoint to be rejected")
	}
	config, err = LoadProviderConfig(func(name string) string {
		switch name {
		case "ITBEM_AI_PROVIDER":
			return "opencode-go"
		case "OPENCODE_GO_API_KEY":
			return "test-key"
		}
		return ""
	})
	if err != nil || config.Provider != ProviderOpenCodeGo || config.Model != "glm-5.3-flash" {
		t.Fatalf("unexpected OpenCode Go config: %#v / %v", config, err)
	}
}

func TestProviderConfigurationRedactsCredentialAndRejectsSecretsInEndpoint(t *testing.T) {
	const sentinel = "provider-key-must-not-appear"
	config, err := NewProviderConfig(ProviderMiniMax, "MiniMax-M3", "https://api.minimax.io/v1/chat/completions", sentinel)
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range []string{fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config), fmt.Sprintf("%#v", config)} {
		if strings.Contains(diagnostic, sentinel) {
			t.Fatalf("provider config diagnostic leaked the credential: %s", diagnostic)
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("provider config JSON leaked the credential: %s", encoded)
	}

	for _, endpoint := range []string{
		"https://user:" + sentinel + "@gateway.example.test/infer",
		"https://gateway.example.test/infer?api_key=" + sentinel,
		"https://gateway.example.test/infer#" + sentinel,
		"https://gateway.example.test/infer?",
	} {
		if err := validateProviderEndpoint(endpoint); err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("endpoint containing URL credentials or secret-bearing metadata was not safely rejected: err=%v", err)
		}
	}
}

func TestGatewayProviderConfigDoesNotLoadProviderKeys(t *testing.T) {
	const sentinel = "provider-key-must-stay-cloud-side"
	config, err := LoadGatewayProviderConfig(func(name string) string {
		switch name {
		case "ITBEM_AI_GATEWAY_URL":
			return "https://gateway.example.test/api/internal/automation/inference"
		case "MINIMAX_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_GO_API_KEY":
			return sentinel
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("local gateway configuration contains a provider credential: %s", encoded)
	}
}

func TestGatewayProviderEndpointsAreOperatorOwned(t *testing.T) {
	for _, sample := range []struct {
		provider Provider
		endpoint string
	}{
		{ProviderMiniMax, "https://api.minimax.io/v1/chat/completions"},
		{ProviderOpenAI, "https://api.openai.com/v1/chat/completions"},
		{ProviderDeepSeek, "https://api.deepseek.com/chat/completions"},
		{ProviderOpenRouter, "https://openrouter.ai/api/v1/chat/completions"},
		{ProviderOpenCodeGo, "https://opencode.ai/zen/go/v1"},
	} {
		endpoint, ok := DefaultProviderEndpoint(sample.provider)
		if !ok || endpoint != sample.endpoint {
			t.Fatalf("operator endpoint for %s = %q / %t", sample.provider, endpoint, ok)
		}
	}
}

func TestProviderClientUsesMiniMaxContractWithoutLeakingSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing authorization")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		thinking, _ := payload["thinking"].(map[string]any)
		if payload["model"] != "MiniMax-M3" || payload["reasoning_split"] != true || thinking["type"] != "disabled" || payload["max_completion_tokens"] != float64(1) {
			t.Fatal("unexpected MiniMax payload")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "response", "model": "MiniMax-M3", "usage": map[string]any{"total_tokens": 2}, "input_sensitive": false, "output_sensitive": false, "base_resp": map[string]any{"status_code": 0}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "ok"}}}})
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL, secret: "test-key"}
	completion, err := newProviderTestClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 1)
	if err != nil || completion.Content != "ok" || completion.Provider != ProviderMiniMax {
		t.Fatalf("unexpected completion: %#v, %v", completion, err)
	}
	metadata, _ := completion.Usage["_itbem_provider"].(map[string]any)
	if metadata["finish_reason"] != "stop" || metadata["input_sensitive"] != false || metadata["output_sensitive"] != false || metadata["status_code"] != int64(0) {
		t.Fatalf("provider outcome metadata was not retained safely: %#v", completion.Usage)
	}
}

func TestMiniMaxThinkingFollowsM3PolicyWithoutChangingM2(t *testing.T) {
	for _, sample := range []struct {
		model            string
		reasoningEnabled bool
		wantDisabled     bool
	}{
		{model: "MiniMax-M3", reasoningEnabled: false, wantDisabled: true},
		{model: "MiniMax-M3", reasoningEnabled: true, wantDisabled: false},
		{model: "MiniMax-M2.7", reasoningEnabled: false, wantDisabled: false},
	} {
		config := ProviderConfig{Provider: ProviderMiniMax, Model: sample.model, ReasoningEnabled: sample.reasoningEnabled, secret: "test-key"}
		payload, _ := (&httpProviderClient{config: config}).payload([]Message{{Role: "user", Content: "Review."}}, 100)
		thinking, exists := payload["thinking"].(map[string]string)
		if exists != sample.wantDisabled || (exists && thinking["type"] != "disabled") {
			t.Fatalf("thinking for %s enabled=%t: %#v", sample.model, sample.reasoningEnabled, payload["thinking"])
		}
	}
}

func TestMiniMaxM3EnabledReasoningReachesTheHTTPAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, disabled := payload["thinking"]; disabled || payload["reasoning_split"] != true || payload["reasoning_effort"] != nil {
			t.Fatal("enabled M3 must retain native thinking without a fabricated effort level")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "MiniMax-M3", "base_resp": map[string]any{"status_code": 0}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "ok"}}}})
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL, ReasoningEnabled: true, secret: "test-key"}
	completion, err := newProviderTestClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "synthetic test"}}, 100)
	if err != nil || completion.Content != "ok" {
		t.Fatalf("enabled M3 adapter completion failed: %v", err)
	}
}

func TestProviderClientsKeepTheSameContractAcrossOpenAICompatibleAndAnthropicAdapters(t *testing.T) {
	for _, sample := range []struct {
		name       string
		provider   Provider
		model      string
		assertBody func(*testing.T, map[string]any)
		respond    func(http.ResponseWriter)
	}{
		{
			name:     "openai",
			provider: ProviderOpenAI,
			model:    "gpt-4.1-mini",
			assertBody: func(t *testing.T, body map[string]any) {
				if body["model"] != "gpt-4.1-mini" || body["max_completion_tokens"] != float64(32) || body["reasoning_split"] != nil {
					t.Fatalf("unexpected OpenAI-compatible payload: %#v", body)
				}
			},
			respond: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "openai-response", "model": "gpt-4.1-mini", "usage": map[string]any{"total_tokens": 4}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "openai answer"}}}})
			},
		},
		{
			name:     "deepseek",
			provider: ProviderDeepSeek,
			model:    "deepseek-flash",
			assertBody: func(t *testing.T, body map[string]any) {
				if body["model"] != "deepseek-flash" || body["max_tokens"] != float64(32) || body["max_completion_tokens"] != nil || body["reasoning_split"] != nil {
					t.Fatalf("unexpected DeepSeek-compatible payload: %#v", body)
				}
			},
			respond: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "deepseek-response", "model": "deepseek-flash", "usage": map[string]any{"total_tokens": 4}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "deepseek answer"}}}})
			},
		},
		{
			name:     "openrouter",
			provider: ProviderOpenRouter,
			model:    "openai/gpt-4.1-mini",
			assertBody: func(t *testing.T, body map[string]any) {
				if body["model"] != "openai/gpt-4.1-mini" || body["max_completion_tokens"] != float64(32) || body["reasoning_split"] != nil {
					t.Fatalf("unexpected OpenRouter-compatible payload: %#v", body)
				}
				if _, supplied := body["temperature"]; supplied {
					t.Fatalf("OpenRouter must omit optional temperature for catalogue compatibility: %#v", body)
				}
				if reasoning := mapValue(body["reasoning"]); reasoning["effort"] != "high" {
					t.Fatalf("OpenRouter reasoning effort was not projected: %#v", body)
				}
			},
			respond: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "openrouter-response", "model": "openai/gpt-4.1-mini", "usage": map[string]any{"total_tokens": 4}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "openrouter answer"}}}})
			},
		},
		{
			name:     "anthropic",
			provider: ProviderAnthropic,
			model:    "claude-test",
			assertBody: func(t *testing.T, body map[string]any) {
				if body["model"] != "claude-test" || body["max_tokens"] != float64(32) || body["system"] != "scope" {
					t.Fatalf("unexpected Anthropic payload: %#v", body)
				}
				messages, _ := body["messages"].([]any)
				if len(messages) != 1 || mapValue(messages[0])["role"] != "user" {
					t.Fatalf("system message must stay in the dedicated field: %#v", messages)
				}
			},
			respond: func(w http.ResponseWriter) {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "anthropic-response", "model": "claude-test", "usage": map[string]any{"input_tokens": 2, "output_tokens": 2}, "content": []any{map[string]any{"type": "text", "text": "anthropic answer"}}})
			},
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if sample.provider == ProviderAnthropic {
					if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("Authorization") != "" {
						t.Fatalf("Anthropic credentials must use x-api-key only")
					}
				} else if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("x-api-key") != "" {
					t.Fatalf("OpenAI-compatible credentials must use Authorization only")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				sample.assertBody(t, body)
				sample.respond(w)
			}))
			defer server.Close()
			config := ProviderConfig{Provider: sample.provider, Model: sample.model, Endpoint: server.URL, secret: "test-key"}
			if sample.provider == ProviderOpenRouter {
				config.ReasoningEnabled, config.ReasoningEffort = true, "high"
			}
			client := newProviderTestClient(config, nil)
			completion, err := client.Complete(context.Background(), []Message{{Role: "system", Content: "scope"}, {Role: "user", Content: "test"}}, 32)
			if err != nil || completion.Provider != sample.provider || completion.Model != sample.model || completion.Content == "" {
				t.Fatalf("provider contract completion failed: %#v / %v", completion, err)
			}
			if err := ValidateProviderCapabilities(client.Capabilities(), "delivery.plan", 32); err != nil {
				t.Fatalf("%s capability contract rejected: %v", sample.name, err)
			}
			audit, err := client.AuditRequest([]Message{{Role: "user", Content: "test"}}, 32)
			if err != nil || strings.Contains(string(audit), "test-key") || strings.Contains(string(audit), server.URL) {
				t.Fatalf("credential-free audit contract failed: %s / %v", audit, err)
			}
		})
	}
}

func TestOpenAIResponsesModelsUseResponsesContractAndParseOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected OpenAI Responses request: %s %#v", r.URL.Path, r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-6-sol" || body["max_output_tokens"] != float64(32) || mapValue(body["reasoning"])["effort"] != "high" {
			t.Fatalf("unexpected OpenAI Responses payload: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "response-1",
			"model": "gpt-6-sol",
			"usage": map[string]any{"input_tokens": 2, "output_tokens": 3},
			"output": []any{map[string]any{
				"content": []any{map[string]any{"type": "output_text", "text": "responses answer"}},
			}},
		})
	}))
	defer server.Close()
	client := newProviderTestClient(ProviderConfig{Provider: ProviderOpenAI, Model: "gpt-6-sol", Endpoint: server.URL + "/chat/completions", ReasoningEnabled: true, ReasoningEffort: "high", secret: "test-key"}, nil)
	completion, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 32)
	if err != nil || completion.Content != "responses answer" {
		t.Fatalf("OpenAI Responses completion=%#v err=%v", completion, err)
	}
}

func TestOpenCodeGoRoutesEachSupportedAPIFamilyWithBoundedMetadata(t *testing.T) {
	for _, sample := range []struct {
		name      string
		model     string
		path      string
		response  map[string]any
		assertion func(*testing.T, map[string]any)
	}{
		{
			name: "chat completions", model: "glm-5.3-flash", path: "/chat/completions",
			response: map[string]any{"id": "chat-1", "model": "glm-5.3-flash", "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3}, "choices": []any{map[string]any{"message": map[string]any{"content": "chat answer"}}}},
			assertion: func(t *testing.T, body map[string]any) {
				if body["max_completion_tokens"] != float64(32) {
					t.Fatalf("chat request was not bounded: %#v", body)
				}
			},
		},
		{
			name: "responses", model: "gpt-6-luna", path: "/responses",
			response: map[string]any{"id": "response-1", "model": "gpt-6-luna", "usage": map[string]any{"input_tokens": 2, "output_tokens": 3}, "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": "responses answer"}}}}},
			assertion: func(t *testing.T, body map[string]any) {
				if body["max_output_tokens"] != float64(32) || body["input"] == nil {
					t.Fatalf("responses request did not use its contract: %#v", body)
				}
			},
		},
		{
			name: "messages", model: "minimax-m3", path: "/messages",
			response: map[string]any{"id": "message-1", "model": "minimax-m3", "usage": map[string]any{"input_tokens": 2, "output_tokens": 3}, "content": []any{map[string]any{"type": "text", "text": "messages answer"}}},
			assertion: func(t *testing.T, body map[string]any) {
				if body["max_tokens"] != float64(32) || body["system"] != "scope" {
					t.Fatalf("messages request did not use its contract: %#v", body)
				}
			},
		},
	} {
		t.Run(sample.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != sample.path || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("X-OpenCode-Session") != "task-1" || r.Header.Get("User-Agent") != "itbem-ai-agent/1.0" {
					t.Fatalf("unexpected OpenCode Go request: path=%q headers=%#v", r.URL.Path, r.Header)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				sample.assertion(t, body)
				_ = json.NewEncoder(w).Encode(sample.response)
			}))
			defer server.Close()
			client := newProviderTestClient(ProviderConfig{Provider: ProviderOpenCodeGo, Model: sample.model, Endpoint: server.URL, SessionID: "task-1", secret: "test-key"}, nil)
			completion, err := client.Complete(context.Background(), []Message{{Role: "system", Content: "scope"}, {Role: "user", Content: "test"}}, 32)
			if err != nil || completion.Content == "" || completion.Provider != ProviderOpenCodeGo {
				t.Fatalf("OpenCode Go completion=%#v err=%v", completion, err)
			}
		})
	}
}

func TestOpenCodeGoRejectsUnknownAPIFamilyBeforeSendingARequest(t *testing.T) {
	_, err := NewProviderConfig(ProviderOpenCodeGo, "omen-alpha", "https://opencode.ai/zen/go/v1", "test-key")
	if err == nil || !strings.Contains(err.Error(), "known API family") {
		t.Fatalf("unknown OpenCode Go model must fail closed: %v", err)
	}
}

func TestProviderClientRetainsMiniMaxUsageForEmptyPolicyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "response-empty", "model": "MiniMax-M3", "input_sensitive": true,
			"usage":   map[string]any{"total_tokens": 9},
			"choices": []any{map[string]any{"message": map[string]any{"content": ""}}},
		})
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL, secret: "test-key"}
	completion, err := newProviderTestClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 1)
	responseError, ok := err.(*ProviderResponseError)
	if !ok || completion.ResponseID != "response-empty" || responseError.Completion.Usage["total_tokens"] != float64(9) {
		t.Fatalf("empty provider response must retain usage and identity: completion=%#v err=%v", completion, err)
	}
	if responseError.Message != "minimax returned an empty response because its safety filter was triggered" {
		t.Fatalf("unexpected empty policy message: %s", responseError.Message)
	}
}

func TestProviderAuditRequestMatchesCredentialFreeWirePayload(t *testing.T) {
	config := ProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: "https://api.minimax.io/v1/chat/completions", secret: "private-test-key"}
	client := newProviderTestClient(config, nil)
	raw, err := client.AuditRequest([]Message{{Role: "system", Content: "scope"}, {Role: "user", Content: "work"}}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || string(raw) == config.secret || string(raw) == config.Endpoint {
		t.Fatalf("audit request must not reveal transport secrets: %s", raw)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "MiniMax-M3" || payload["reasoning_split"] != true || payload["max_completion_tokens"] != float64(1024) {
		t.Fatalf("audit payload does not match the bounded MiniMax wire contract: %#v", payload)
	}
}

func TestProviderCapabilitiesAreVersionedAndBounded(t *testing.T) {
	config := ProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: "https://api.minimax.io/v1/chat/completions", secret: "private-test-key"}
	client := NewProviderClient(config, nil)
	reader, ok := client.(ProviderCapabilityReader)
	if !ok {
		t.Fatal("HTTP provider must expose its capability contract")
	}
	capabilities := reader.Capabilities()
	if err := ValidateProviderCapabilities(capabilities, "delivery.implementation", miniMaxM3CompletionLimit); err != nil {
		t.Fatalf("built-in MiniMax contract must admit the bounded implementation operation: %v", err)
	}
	capabilities.SupportsJSONActions = false
	if err := ValidateProviderCapabilities(capabilities, "delivery.plan", DefaultCompletionTokens); err == nil {
		t.Fatal("provider without JSON-action guarantee must be rejected before inference")
	}
	capabilities = reader.Capabilities()
	capabilities.MaxCompletionTokens = DefaultCompletionTokens - 1
	if err := ValidateProviderCapabilities(capabilities, "delivery.plan", DefaultCompletionTokens); err == nil {
		t.Fatal("provider below the operation token bound must be rejected")
	}
}

func TestMiniMaxCompletionTokensAreBoundedByModel(t *testing.T) {
	if value, err := boundedCompletionTokens(ProviderMiniMax, "MiniMax-M2.7", 0); err != nil || value != miniMaxM2CompletionLimit {
		t.Fatalf("default MiniMax M2 token bound: %d / %v", value, err)
	}
	if value, err := boundedCompletionTokens(ProviderMiniMax, "MiniMax-M3", 0); err != nil || value != DefaultCompletionTokens {
		t.Fatalf("default MiniMax M3 token bound: %d / %v", value, err)
	}
	if value, err := boundedCompletionTokens(ProviderMiniMax, "MiniMax-M2.7", 100000); err != nil || value != miniMaxM2CompletionLimit {
		t.Fatalf("unexpected MiniMax M2 token bound: %d / %v", value, err)
	}
	if value, err := boundedCompletionTokens(ProviderMiniMax, "MiniMax-M3", 100000); err != nil || value != miniMaxM3CompletionLimit {
		t.Fatalf("unexpected MiniMax M3 token bound: %d / %v", value, err)
	}
	if value, err := boundedCompletionTokens(ProviderOpenAI, "test", 100000); err != nil || value != 100000 {
		t.Fatalf("unexpected OpenAI token bound: %d / %v", value, err)
	}
}

func TestCompletionTokensRespectModelAndGlobalOutputLimitsWithoutTruncatingExplicitValues(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		model    string
		limit    int
		request  int
		want     int
		wantErr  bool
	}{
		{name: "openai exact model limit", provider: ProviderOpenAI, model: "gpt-test", limit: 64, request: 64, want: 64},
		{name: "openai one over model limit", provider: ProviderOpenAI, model: "gpt-test", limit: 64, request: 65, wantErr: true},
		{name: "deepseek one over model limit", provider: ProviderDeepSeek, model: "deepseek-flash", limit: 32, request: 33, wantErr: true},
		{name: "openrouter exact model limit", provider: ProviderOpenRouter, model: "vendor/model", limit: 96, request: 96, want: 96},
		{name: "minimax m3 adapter is stricter", provider: ProviderMiniMax, model: "MiniMax-M3", limit: 65_536, request: miniMaxM3CompletionLimit + 1, wantErr: true},
		{name: "minimax m2 adapter is stricter", provider: ProviderMiniMax, model: "MiniMax-M2.7", limit: 4_096, request: miniMaxM2CompletionLimit + 1, wantErr: true},
		{name: "global cap is stricter than model metadata", provider: ProviderOpenAI, model: "gpt-large", limit: 200_000, request: MaxCompletionTokens + 1, wantErr: true},
		{name: "implicit default resolves to smaller model output", provider: ProviderOpenAI, model: "gpt-small", limit: 128, request: 0, want: 128},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := completionTokensWithinModelLimits(test.provider, test.model, test.request, inferenceModelLimits{ContextWindowTokens: 1_000_000, MaxOutputTokens: test.limit})
			if test.wantErr {
				if err == nil || strings.Contains(err.Error(), test.model) {
					t.Fatalf("expected a generic model-limit rejection, got %v", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("completion token bound = %d / %v, want %d", got, err, test.want)
			}
		})
	}
}

func TestProviderClientRejectsContextAndOutputOveragesBeforeProviderRequest(t *testing.T) {
	const key = "model-limit-test-key"
	var providerCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["max_completion_tokens"] != float64(8) {
			t.Errorf("provider received an unexpected output limit: %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "response", "model": "gpt-4o-mini", "choices": []any{map[string]any{"message": map[string]any{"content": "ok"}}}})
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderOpenAI, Model: "gpt-4o-mini", Endpoint: server.URL, secret: key}
	client := newProviderTestClient(config, server.Client())
	client.resolveLimits = func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error) {
		return inferenceModelLimits{ContextWindowTokens: 400, MaxOutputTokens: 8}, nil
	}

	boundary := []Message{{Role: "user", Content: strings.Repeat("x", 100)}}
	if _, err := client.Complete(context.Background(), boundary, 8); err != nil {
		t.Fatalf("exact input-plus-output context boundary should be accepted: %v", err)
	}
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: strings.Repeat("x", 101)}}, 8); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("one byte beyond the conservative context bound should fail without exposing credentials: %v", err)
	}
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "short"}}, 9); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("output above the model maximum should fail without exposing credentials: %v", err)
	}
	for _, test := range []struct {
		provider Provider
		model    string
	}{
		{ProviderDeepSeek, "deepseek-flash"},
		{ProviderOpenRouter, "vendor/model"},
		{ProviderMiniMax, "MiniMax-M3"},
	} {
		providerClient := newProviderTestClient(ProviderConfig{Provider: test.provider, Model: test.model, Endpoint: server.URL, secret: key}, server.Client())
		providerClient.resolveLimits = func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error) {
			return inferenceModelLimits{ContextWindowTokens: 400, MaxOutputTokens: 8}, nil
		}
		if _, err := providerClient.Complete(context.Background(), []Message{{Role: "user", Content: strings.Repeat("x", 101)}}, 8); err == nil {
			t.Errorf("%s accepted a prompt above the model context window", test.provider)
		}
		if _, err := providerClient.Complete(context.Background(), []Message{{Role: "user", Content: "short"}}, 9); err == nil {
			t.Errorf("%s accepted output above the model maximum", test.provider)
		}
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider was called %d times; only the exact-boundary request may be sent", providerCalls.Load())
	}
}

func TestProviderClientRejectsMissingModelMetadataBeforeProviderRequest(t *testing.T) {
	const secret = "model-limit-metadata-secret"
	var providerCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer server.Close()
	client := newProviderTestClient(ProviderConfig{Provider: ProviderDeepSeek, Model: "deepseek-flash", Endpoint: server.URL, secret: secret}, server.Client())
	client.resolveLimits = func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error) {
		return inferenceModelLimits{}, fmt.Errorf("upstream metadata echoed %s", secret)
	}
	_, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "private prompt"}}, 8)
	if err == nil || err.Error() != "provider model limits are unavailable" || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "private prompt") {
		t.Fatalf("missing model metadata must fail closed with a generic error: %v", err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider received a request despite missing model metadata: %d", providerCalls.Load())
	}
}

func TestProviderClientMarksRateLimitsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderOpenAI, Model: "gpt-4o-mini", Endpoint: server.URL, secret: "test-key"}
	_, err := newProviderTestClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 256)
	retryable, ok := err.(*RetryableError)
	if !ok || retryable.RetryAfter != 90*time.Second {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestProviderClientDoesNotExposeProviderErrorBody(t *testing.T) {
	const sentinel = "provider-key-must-not-appear-in-error"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + sentinel + `"}`))
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderOpenAI, Model: "gpt-4.1-mini", Endpoint: server.URL, secret: sentinel}
	_, err := newProviderTestClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 32)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("provider error must expose only bounded status, got %v", err)
	}
}

func TestProviderClientRejectsRedirectsWithoutForwardingCredentialOrMutatingSharedClient(t *testing.T) {
	const sentinel = "provider-redirect-secret-sentinel"
	var destinationRequests atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationRequests.Add(1)
		if r.Header.Get("Authorization") == "Bearer "+sentinel {
			t.Error("provider credential was forwarded to a redirect destination")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"unexpected"}}]}`))
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	sharedClient := origin.Client()
	var callerRedirectPolicyCalls atomic.Int64
	sharedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		callerRedirectPolicyCalls.Add(1)
		return nil
	}
	config := ProviderConfig{Provider: ProviderOpenAI, Model: "gpt-4.1-mini", Endpoint: origin.URL, secret: sentinel}
	_, err := newProviderTestClient(config, sharedClient).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 32)
	if err == nil || !strings.Contains(err.Error(), "307") || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("redirect must fail closed without exposing the credential, got %v", err)
	}
	if destinationRequests.Load() != 0 {
		t.Fatalf("redirect destination received %d requests", destinationRequests.Load())
	}
	if callerRedirectPolicyCalls.Load() != 0 {
		t.Fatalf("provider client invoked the caller's redirect policy %d times", callerRedirectPolicyCalls.Load())
	}
	if err := sharedClient.CheckRedirect(&http.Request{}, nil); err != nil || callerRedirectPolicyCalls.Load() != 1 {
		t.Fatalf("constructor mutated the shared caller client: callback count=%d err=%v", callerRedirectPolicyCalls.Load(), err)
	}
}

func TestProviderClientRedactsCredentialEchoFromCompletionAndUsage(t *testing.T) {
	const sentinel = "provider-key-echo-sentinel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+sentinel {
			t.Fatal("test provider did not receive its configured authorization header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      sentinel,
			"model":   "model-" + sentinel,
			"usage":   map[string]any{"diagnostic": sentinel, "nested": []any{map[string]any{"value": sentinel}}},
			"choices": []any{map[string]any{"message": map[string]any{"content": "answer " + sentinel}}},
		})
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderOpenAI, Model: "gpt-4.1-mini", Endpoint: server.URL, secret: sentinel}
	completion, err := newProviderTestClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 32)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(completion)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("provider credential echoed by the upstream appeared in the local completion: %s", encoded)
	}
	if !strings.Contains(completion.Content, "[REDACTED]") || !strings.Contains(completion.ResponseID, "[REDACTED]") {
		t.Fatalf("completion identifiers/content did not pass through credential redaction: %#v", completion)
	}
}

func TestProviderRetryAfterClampsProviderHints(t *testing.T) {
	now := time.Date(2026, time.August, 10, 4, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		value string
		want  time.Duration
	}{
		{"", providerRetryDefaultDelay}, {"1", providerRetryMinDelay}, {"99999", providerRetryMaxDelay},
		{now.Add(75 * time.Second).Format(http.TimeFormat), 75 * time.Second}, {"not-a-delay", providerRetryDefaultDelay},
	} {
		header := http.Header{}
		header.Set("Retry-After", sample.value)
		if got := providerRetryAfter(header, now); got != sample.want {
			t.Fatalf("retry after %q = %s, want %s", sample.value, got, sample.want)
		}
	}
}
