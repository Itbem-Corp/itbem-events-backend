package automationagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProviderConfigDefaultsToMiniMaxM3AndRejectsUnsafeEndpoints(t *testing.T) {
	config, err := LoadProviderConfig(func(name string) string {
		if name == "MINIMAX_API_KEY" {
			return "test-key"
		}
		return ""
	})
	if err != nil || config.Provider != ProviderMiniMax || config.Model != "MiniMax-M3" || config.requestTimeout != providerRequestTimeout {
		t.Fatalf("unexpected config: %#v, %v", config, err)
	}
	configured, err := LoadProviderConfig(func(name string) string {
		if name == "MINIMAX_API_KEY" {
			return "test-key"
		}
		if name == "ITBEM_AI_PROVIDER_TIMEOUT_SECONDS" {
			return "300"
		}
		return ""
	})
	if err != nil || configured.requestTimeout != 5*time.Minute {
		t.Fatalf("unexpected configured provider timeout: %#v, %v", configured, err)
	}
	_, err = LoadProviderConfig(func(name string) string {
		if name == "MINIMAX_API_KEY" {
			return "test-key"
		}
		if name == "ITBEM_AI_PROVIDER_TIMEOUT_SECONDS" {
			return "901"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected excessive provider timeout to be rejected")
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
		if payload["model"] != "MiniMax-M3" || payload["reasoning_split"] != true || payload["thinking"] != nil || payload["max_completion_tokens"] != float64(1) {
			t.Fatal("unexpected MiniMax payload")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "response", "model": "MiniMax-M3", "usage": map[string]any{"total_tokens": 2}, "input_sensitive": false, "output_sensitive": false, "base_resp": map[string]any{"status_code": 0}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "ok"}}}})
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL, secret: "test-key"}
	completion, err := NewProviderClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 1)
	if err != nil || completion.Content != "ok" || completion.Provider != ProviderMiniMax {
		t.Fatalf("unexpected completion: %#v, %v", completion, err)
	}
	metadata, _ := completion.Usage["_itbem_provider"].(map[string]any)
	if metadata["finish_reason"] != "stop" || metadata["input_sensitive"] != false || metadata["output_sensitive"] != false || metadata["status_code"] != int64(0) {
		t.Fatalf("provider outcome metadata was not retained safely: %#v", completion.Usage)
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
	completion, err := NewProviderClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 1)
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
	client := NewProviderClient(config, nil)
	auditor, ok := client.(ProviderRequestAuditor)
	if !ok {
		t.Fatal("HTTP provider must expose an audit serializer")
	}
	raw, err := auditor.AuditRequest([]Message{{Role: "system", Content: "scope"}, {Role: "user", Content: "work"}}, miniMaxM3CompletionLimit+1)
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
	if payload["model"] != "MiniMax-M3" || payload["reasoning_split"] != true || payload["max_completion_tokens"] != float64(miniMaxM3CompletionLimit) {
		t.Fatalf("audit payload does not match the bounded MiniMax wire contract: %#v", payload)
	}
}

func TestMiniMaxCompletionTokensAreBoundedByModel(t *testing.T) {
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

func TestProviderClientMarksRateLimitsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	config := ProviderConfig{Provider: ProviderOpenAI, Model: "test", Endpoint: server.URL, secret: "test-key"}
	_, err := NewProviderClient(config, nil).Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, 256)
	retryable, ok := err.(*RetryableError)
	if !ok || retryable.RetryAfter != 90*time.Second {
		t.Fatalf("expected retryable error, got %v", err)
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
