package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

const testGatewayCapabilitySigningKey = "gateway-provider-unit-test-server-signing-key-48-bytes"

func TestGatewayLostResponseDoesNotAuthorizeAnotherBillableAttempt(t *testing.T) {
	for _, mode := range []string{"timeout", "connection_reset"} {
		t.Run(mode, func(t *testing.T) {
			installGatewayTestCapability(t, "task-unknown", "run-unknown", "code.review")
			var admitted atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				admitted.Add(1) // The gateway may already have invoked a provider.
				if mode == "timeout" {
					<-r.Context().Done()
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			httpClient := server.Client()
			httpClient.Timeout = 100 * time.Millisecond
			client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL + "/private-marker"}, httpClient)
			_, err := client.Complete(WithInferenceLease(context.Background(), "task-unknown", "run-unknown", "code.review", ""), []Message{{Role: "user", Content: "fixture"}}, 32)
			if err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatalf("unsafe or absent error: %v", err)
			}
			var retryable *RetryableError
			if errors.As(err, &retryable) {
				t.Fatalf("lost response authorized a new paid attempt: %v", err)
			}
			if admitted.Load() != 1 {
				t.Fatalf("expected one potentially billed request, got %d", admitted.Load())
			}
		})
	}
}
func TestGatewayUnresolvedFailuresDoNotAuthorizeAutomaticRetry(t *testing.T) {
	for _, code := range []string{"accounting_db_22001", "accounting_deadline", "accounting_usage_unverified", "provider_http_502", "accounting_db_22001 private", "provider_unclassified", "provider_model_limits_unavailable", "credentials_unavailable", "routing_invalid", ""} {
		t.Run(code, func(t *testing.T) {
			installGatewayTestCapability(t, "task-accounting", "run-accounting", "code.review")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(InferenceFailureHeader, code)
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("private provider body"))
			}))
			defer server.Close()
			client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL}, server.Client())
			_, err := client.Complete(WithInferenceLease(context.Background(), "task-accounting", "run-accounting", "code.review", ""), []Message{{Role: "user", Content: "fixture"}}, 32)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("missing or unsafe error: %v", err)
			}
			var retryable *RetryableError
			wantRetryable := code == "provider_model_limits_unavailable" || code == "credentials_unavailable" || code == "routing_invalid"
			if errors.As(err, &retryable) != wantRetryable {
				t.Fatalf("retry classification incorrect for %q: %v", code, err)
			}
		})
	}
}

func newGatewayProviderTestClient(config GatewayProviderConfig, client *http.Client) *gatewayProviderClient {
	provider := NewGatewayProviderClient(config, client).(*gatewayProviderClient)
	provider.resolveLimits = func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error) {
		return inferenceModelLimits{ContextWindowTokens: 1_000_000, MaxOutputTokens: MaxCompletionTokens}, nil
	}
	return provider
}

func installGatewayTestCapability(t *testing.T, taskID, runID, operation string) string {
	t.Helper()
	scope := inferencecapability.Scope{
		TaskID: taskID, RunID: runID, Operation: operation,
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
	}
	token, err := inferencecapability.Mint(testGatewayCapabilitySigningKey, scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeInferenceCapability(taskID, runID, token, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clearInferenceCapabilitiesForRun(taskID, runID) })
	return token
}

func TestGatewayProviderSendsServerCapabilityAndNeverProviderOrCallbackCredential(t *testing.T) {
	capability := installGatewayTestCapability(t, "task-1", "run-1", "delivery.plan")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(inferencecapability.HeaderName) != capability {
			t.Fatal("gateway server-issued run capability missing")
		}
		if r.Header.Get("X-Automation-Secret") != "" {
			t.Fatal("gateway request must not carry the worker callback master")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			CallID     string `json:"call_id"`
			PlanStepID string `json:"plan_step_id"`
		}
		if err := json.Unmarshal(body, &request); err != nil || !validReceiptUUID(request.CallID) || strings.Contains(string(body), capability) || !strings.Contains(string(body), `"task_id":"task-1"`) || !strings.Contains(string(body), `"run_id":"run-1"`) {
			t.Fatalf("gateway request leaked a bearer token or omitted its lease: %s", body)
		}
		if request.PlanStepID != "" {
			t.Fatalf("task-level planning inference must not invent a step binding: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"minimax","model":"MiniMax-M3","content":"ok","response_id":"response-1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"call_id":"` + request.CallID + `","receipt_id":"` + uuid.Must(uuid.NewV4()).String() + `"}`))
	}))
	defer server.Close()

	client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL}, server.Client())
	ctx := WithInferenceLease(context.Background(), "task-1", "run-1", "delivery.plan", "")
	completion, err := client.Complete(ctx, []Message{{Role: "user", Content: "hello"}}, 32)
	if err != nil || completion.Content != "ok" || completion.Provider != ProviderMiniMax {
		t.Fatalf("completion=%#v err=%v", completion, err)
	}
}

func TestGatewayProviderSendsOnlyOpaqueActivePlanStepID(t *testing.T) {
	const taskID, runID, stepID = "task-implementation", "run-implementation", "29b04756-e851-47c0-a989-1e3b84eb8c39"
	installGatewayTestCapability(t, taskID, runID, "delivery.implementation")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			CallID     string `json:"call_id"`
			PlanStepID string `json:"plan_step_id"`
		}
		if err := json.Unmarshal(body, &request); err != nil || request.PlanStepID != stepID {
			t.Fatalf("active inference did not carry its opaque plan-step binding: %s", body)
		}
		for _, forbidden := range []string{"provider_key", "api_key", "authorization", "reasoning_content", "hidden_reasoning"} {
			if strings.Contains(strings.ToLower(string(body)), forbidden) {
				t.Fatalf("inference request included fields beyond its opaque step binding: %s", body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"minimax","model":"MiniMax-M3","content":"ok","response_id":"response-1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"call_id":"` + request.CallID + `","receipt_id":"` + uuid.Must(uuid.NewV4()).String() + `"}`))
	}))
	defer server.Close()
	client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL}, server.Client())
	ctx := WithInferenceLease(context.Background(), taskID, runID, "delivery.implementation", stepID)
	if _, err := client.Complete(ctx, []Message{{Role: "user", Content: "hello"}}, 32); err != nil {
		t.Fatalf("gateway rejected a valid opaque step-bound request: %v", err)
	}
}

func TestGatewayProviderFailsClosedWithoutLeaseOrServerCapability(t *testing.T) {
	client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: "https://gateway.example.test"}, nil)
	if _, err := client.Complete(context.Background(), []Message{{Role: "user", Content: "hello"}}, 32); err == nil || !strings.Contains(err.Error(), "execution lease") {
		t.Fatalf("gateway accepted an unbound call: %v", err)
	}
	ctx := WithInferenceLease(context.Background(), "missing-capability-task", "missing-capability-run", "delivery.plan", "")
	if _, err := client.Complete(ctx, []Message{{Role: "user", Content: "hello"}}, 32); err == nil || !strings.Contains(err.Error(), "server-issued") {
		t.Fatalf("gateway accepted a lease without server capability: %v", err)
	}
}

func TestGatewayProviderDoesNotForwardCapabilityAcrossRedirect(t *testing.T) {
	capability := installGatewayTestCapability(t, "task-redirect", "run-redirect", "delivery.plan")
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(inferencecapability.HeaderName) != "" {
			t.Error("gateway capability was forwarded to a redirect target")
		}
		t.Error("gateway client followed a redirect to another host")
	}))
	defer redirectTarget.Close()

	redirectingGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(inferencecapability.HeaderName) != capability {
			t.Error("initial gateway request did not carry its server capability")
		}
		if r.Header.Get("X-Automation-Secret") != "" {
			t.Error("gateway request carried the callback master")
		}
		http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer redirectingGateway.Close()

	providedClient := redirectingGateway.Client()
	client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: redirectingGateway.URL}, providedClient)
	ctx := WithInferenceLease(context.Background(), "task-redirect", "run-redirect", "delivery.plan", "")
	_, err := client.Complete(ctx, []Message{{Role: "user", Content: "hello"}}, 32)
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("gateway redirect should be returned without following it: %v", err)
	}
	if providedClient.CheckRedirect != nil {
		t.Fatal("gateway constructor unexpectedly mutated the caller's shared HTTP client")
	}
}

func TestGatewayProviderDoesNotExposeProviderErrorBodyToWorker(t *testing.T) {
	const sentinel = "provider-key-or-error-detail-sentinel"
	installGatewayTestCapability(t, "task-error", "run-error", "delivery.plan")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"` + sentinel + `"}`))
	}))
	defer server.Close()
	client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL}, nil)
	ctx := WithInferenceLease(context.Background(), "task-error", "run-error", "delivery.plan", "")
	_, err := client.Complete(ctx, []Message{{Role: "user", Content: "hello"}}, 32)
	if err == nil || !strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("gateway must expose only bounded status, not provider body: %v", err)
	}
}

func TestGatewayProviderExposesOnlyKnownConflictClass(t *testing.T) {
	for _, reason := range []string{
		InferenceConflictPolicyUnavailable,
		InferenceConflictPolicyScopeInvalid,
		InferenceConflictPolicyInvalid,
		InferenceConflictPolicySignatureInvalid,
		InferenceConflictPolicyRoutesMissing,
		InferenceConflictPolicyRoutesInvalid,
	} {
		t.Run(reason, func(t *testing.T) {
			installGatewayTestCapability(t, "task-conflict", "run-conflict", "code.review")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(InferenceConflictHeader, reason)
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":"private database detail"}`))
			}))
			defer server.Close()
			client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderMiniMax, Model: "MiniMax-M3", Endpoint: server.URL}, server.Client())
			ctx := WithInferenceLease(context.Background(), "task-conflict", "run-conflict", "code.review", "")
			_, err := client.Complete(ctx, []Message{{Role: "user", Content: "hello"}}, 32)
			if err == nil || err.Error() != "AI gateway request rejected (409: "+reason+")" || strings.Contains(err.Error(), "database") {
				t.Fatalf("gateway conflict diagnostic was not safely bounded: %v", err)
			}
		})
	}
}

func TestGatewayProviderRejectsModelContextAndOutputOveragesBeforeGatewayRequest(t *testing.T) {
	installGatewayTestCapability(t, "task-model-limits", "run-model-limits", "delivery.plan")
	var gatewayCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		gatewayCalls.Add(1)
	}))
	defer server.Close()
	client := newGatewayProviderTestClient(GatewayProviderConfig{Provider: ProviderOpenAI, Model: "gpt-4.1-mini", Endpoint: server.URL}, server.Client())
	client.resolveLimits = func(context.Context, Provider, string, string, *http.Client) (inferenceModelLimits, error) {
		return inferenceModelLimits{ContextWindowTokens: 400, MaxOutputTokens: 8}, nil
	}
	ctx := WithInferenceLease(context.Background(), "task-model-limits", "run-model-limits", "delivery.plan", "")
	if _, err := client.Complete(ctx, []Message{{Role: "user", Content: strings.Repeat("x", 101)}}, 8); err == nil {
		t.Fatal("gateway accepted a prompt above the model context window")
	}
	if _, err := client.Complete(ctx, []Message{{Role: "user", Content: "short"}}, 9); err == nil {
		t.Fatal("gateway accepted output above the model maximum")
	}
	if gatewayCalls.Load() != 0 {
		t.Fatalf("gateway received %d requests for model-limit violations", gatewayCalls.Load())
	}
}
