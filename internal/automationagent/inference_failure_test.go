package automationagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInferenceFailureCodesNeverExposeArbitraryProviderText(t *testing.T) {
	for _, raw := range []string{"private-key-marker", "provider_http_400?token=private", "provider_http_200", "provider_http_0400", "credentials_unavailable private"} {
		if SafeInferenceFailureCode(raw) != "" {
			t.Fatalf("unsafe code accepted: %q", raw)
		}
	}
	for _, scenario := range []struct {
		err  error
		want string
	}{
		{&ProviderHTTPError{StatusCode: 402}, "provider_http_402"},
		{fmt.Errorf("wrapped: %w", &ProviderHTTPError{StatusCode: 400}), "provider_http_400"},
		{&RetryableError{StatusCode: 429}, "provider_http_429"},
		{errors.New("provider model limits are unavailable"), "provider_model_limits_unavailable"},
		{errors.New("provider body with private-key-marker"), "provider_unclassified"},
	} {
		if got := InferenceFailureCode(scenario.err); got != scenario.want {
			t.Fatalf("got %q, want %q", got, scenario.want)
		}
	}
}

func TestGatewayFailureDiagnosticsRemainBoundedAndKeepRetrySemantics(t *testing.T) {
	const taskID, runID = "diagnostic-task", "diagnostic-run"
	installGatewayTestCapability(t, taskID, runID, "ai.chat")
	for _, code := range []string{"provider_model_limits_unavailable", "provider_http_402", "private-key-marker"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(InferenceFailureHeader, code)
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("private-response-body-marker"))
			}))
			defer server.Close()
			client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL}, server.Client())
			_, err := client.Complete(WithInferenceLease(context.Background(), taskID, runID, "ai.chat", ""), []Message{{Role: "user", Content: "synthetic"}}, 32)
			var retryable *RetryableError
			if !errors.As(err, &retryable) || retryable.StatusCode != 502 {
				t.Fatalf("retry semantics changed: %v", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("diagnostic leaked arbitrary content")
			}
			if SafeInferenceFailureCode(code) != "" && !strings.Contains(err.Error(), code) {
				t.Fatal("safe diagnostic was omitted")
			}
		})
	}
}
