package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentprotocol"
	"events-stocks/internal/inferencecapability"
	"github.com/gofrs/uuid"
)

func TestHTTPCallbackRetainsOnlyAnExplicitExpiredReservationConflict(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set(retryReservationHeader, "1")
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	callback := newTestHTTPCallback(t, server)
	updated, err := callback.Update(context.Background(), "d4a4b837-2e18-43af-9f58-6d59629db2bb", TaskUpdate{Status: "completed"})
	if updated {
		t.Fatal("expired reservation callback must not be accepted")
	}
	var retryable *RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("expected retryable expired reservation error, got %v", err)
	}
}

func TestHTTPCallbackDoesNotRetryOrdinaryConflict(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	callback := newTestHTTPCallback(t, server)
	updated, err := callback.Update(context.Background(), "d4a4b837-2e18-43af-9f58-6d59629db2bb", TaskUpdate{Status: "completed"})
	if updated || err != nil {
		t.Fatalf("ordinary callback conflict = updated:%v err:%v, want ignored", updated, err)
	}
}

func TestHTTPCallbackReportsOnlyAllowlistedClaimRejectionReasons(t *testing.T) {
	t.Parallel()

	for _, scenario := range []struct {
		name     string
		body     string
		expected string
	}{
		{name: "known reason", body: `{"error":"worker identity or profile is invalid"}`, expected: "worker identity or profile is invalid"},
		{name: "known category", body: `{"message":"Invalid automation result","error":"private diagnostic"}`, expected: "automation result payload is invalid"},
		{name: "unknown reason", body: `{"error":"secret database diagnostic"}`},
		{name: "invalid JSON", body: `not-json`},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(scenario.body))
			}))
			defer server.Close()
			callback := newTestHTTPCallback(t, server)
			_, err := callback.Update(context.Background(), "task", TaskUpdate{Status: "running", RunID: "run"})
			if err == nil {
				t.Fatal("rejected callback must return an error")
			}
			if scenario.expected != "" && !strings.Contains(err.Error(), scenario.expected) {
				t.Fatalf("error %q does not contain allowlisted reason %q", err, scenario.expected)
			}
			if scenario.expected == "" && err.Error() != "automation callback rejected (400)" {
				t.Fatalf("unknown response detail leaked into error: %q", err)
			}
		})
	}
}

func TestMachineCallbackSignatureBindsExactPathAndBody(t *testing.T) {
	identity, instanceID := newTestMachineIdentity(t)
	callback, err := NewHTTPCallback("http://127.0.0.1:18080", identity, instanceID, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"status":"completed"}`)
	request, err := callback.newSignedRequest(context.Background(), http.MethodPut, callback.baseURL+"/api/internal/automation/tasks/abc?next=%2Fsafe", body)
	if err != nil {
		t.Fatal(err)
	}
	timestamp, err := strconv.ParseInt(request.Header.Get(agentcallbackauth.TimestampHeader), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := agentcallbackauth.DecodeSignature(request.Header.Get(agentcallbackauth.SignatureHeader))
	if err != nil {
		t.Fatal(err)
	}
	verify := func(requestURI string, payload []byte) error {
		return agentcallbackauth.VerifyRequest(identity.PublicKey(), signature, instanceID, request.Method, requestURI, timestamp, request.Header.Get(agentcallbackauth.NonceHeader), payload, time.Now().UTC())
	}
	if err := verify(request.URL.RequestURI(), body); err != nil {
		t.Fatalf("valid signed callback rejected: %v", err)
	}
	if err := verify(request.URL.RequestURI()+"&tampered=1", body); err == nil {
		t.Fatal("callback signature accepted a modified path/query")
	}
	if err := verify(request.URL.RequestURI(), []byte(`{"status":"failed"}`)); err == nil {
		t.Fatal("callback signature accepted a modified body")
	}
}

func TestHTTPCallbackRetainsBusyRunClaim(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-ITBEM-Automation-Run-Busy", "1")
		writer.Header().Set(retryLeaseHeader, "37")
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	callback := newTestHTTPCallback(t, server)
	accepted, err := callback.Update(context.Background(), "task", TaskUpdate{Status: "running", RunID: "new-run"})
	var retryable *RetryableError
	if accepted || !errors.As(err, &retryable) {
		t.Fatalf("busy claim must retain its delivery, got accepted=%v error=%v", accepted, err)
	}
	if retryable.RetryAfter != 37*time.Second {
		t.Fatalf("busy lease retry = %s, want the server-provided remaining lease", retryable.RetryAfter)
	}
	// A stale terminal callback must never become a fresh provider run merely
	// because a proxy accidentally retained the claim-only response header.
	accepted, err = callback.Update(context.Background(), "task", TaskUpdate{Status: "completed", RunID: "old-run"})
	if accepted || err != nil {
		t.Fatalf("stale terminal callback must remain ignored: accepted=%v error=%v", accepted, err)
	}
}

func TestHTTPCallbackBusyRunRejectsUntrustedRetryLeaseValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "0", "-1", "not-a-number", "999999999"} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set(runBusyHeader, "1")
				writer.Header().Set(retryLeaseHeader, value)
				writer.WriteHeader(http.StatusConflict)
			}))
			defer server.Close()
			callback := newTestHTTPCallback(t, server)
			_, err := callback.Update(context.Background(), "task", TaskUpdate{Status: "running", RunID: "new-run"})
			var retryable *RetryableError
			if !errors.As(err, &retryable) || retryable.RetryAfter != providerRetryMaxDelay {
				t.Fatalf("retry value %q produced %#v, want safe maximum", value, err)
			}
		})
	}
}

func TestHTTPCallbackRequiresServerCapabilityForRunAndClearsOnCompletion(t *testing.T) {
	const taskID = "task-capability-cache"
	const runID = "run-capability-cache"
	const signingKey = "server-only-inference-signing-key-48-bytes"
	identity, instanceID := newTestMachineIdentity(t)
	scope := inferencecapability.Scope{
		TaskID: taskID, RunID: runID, Operation: "delivery.plan",
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
	}
	token, err := inferencecapability.Mint(signingKey, scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer clearInferenceCapabilitiesForRun(taskID, runID)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		assertSignedCallbackRequest(t, request, body, identity, instanceID)
		if request.Method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/tasks/"+taskID) {
			var update TaskUpdate
			if err := json.Unmarshal(body, &update); err != nil {
				t.Fatal(err)
			}
			if update.Status == "running" {
				writer.Header().Set(inferencecapability.HeaderName, token)
			}
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	accepted, err := callback.Update(context.Background(), taskID, TaskUpdate{Status: "running", RunID: runID})
	if err != nil || !accepted {
		t.Fatalf("server-issued capability claim accepted=%v err=%v", accepted, err)
	}
	if cached, ok := inferenceCapabilityForRun(taskID, runID, time.Now().UTC()); !ok || cached != token {
		t.Fatal("capability response header was not retained only in the worker's in-memory run cache")
	}
	ctx := WithInferenceLease(context.Background(), taskID, runID, "delivery.plan", "")
	if fromContext, ok := InferenceCapabilityFromContext(ctx); !ok || fromContext != token {
		t.Fatal("provider inference context did not bind the claim capability")
	}
	accepted, err = callback.Update(context.Background(), taskID, TaskUpdate{Status: "completed", RunID: runID})
	if err != nil || !accepted {
		t.Fatalf("terminal callback accepted=%v err=%v", accepted, err)
	}
	if _, ok := inferenceCapabilityForRun(taskID, runID, time.Now().UTC()); ok {
		t.Fatal("terminal callback did not clear its run capability")
	}
}

func TestHTTPCallbackRejectsSuccessfulClaimWithoutServerIssuedCapability(t *testing.T) {
	const taskID = "task-no-capability"
	const runID = "run-no-capability"
	defer clearInferenceCapabilitiesForRun(taskID, runID)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	callback := newTestHTTPCallback(t, server)
	accepted, err := callback.Update(context.Background(), taskID, TaskUpdate{Status: "running", RunID: runID})
	if accepted || err == nil {
		t.Fatalf("callback accepted a claim without server capability: accepted=%v err=%v", accepted, err)
	}
	if _, ok := inferenceCapabilityForRun(taskID, runID, time.Now().UTC()); ok {
		t.Fatal("callback cached a missing capability")
	}
}

func TestHTTPCallbackNeverForwardsSecretAcrossRedirect(t *testing.T) {
	t.Parallel()

	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		destinationHits.Add(1)
		if request.Header.Get("X-Automation-Secret") != "" {
			t.Error("legacy callback shared secret was forwarded to redirect destination")
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL+"/collect", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	identity, instanceID := newTestMachineIdentity(t)
	callback, err := NewHTTPCallback(source.URL, identity, instanceID, source.Client())
	if err != nil {
		t.Fatalf("NewHTTPCallback: %v", err)
	}
	if _, err := callback.Update(context.Background(), "task", TaskUpdate{Status: "completed"}); err == nil {
		t.Fatal("redirect must be rejected as a failed callback")
	}
	if destinationHits.Load() != 0 {
		t.Fatalf("redirect destination requests = %d, want 0", destinationHits.Load())
	}
}

type taskCallbackRecorder struct {
	updates []TaskUpdate
}

func (recorder *taskCallbackRecorder) Update(_ context.Context, _ string, update TaskUpdate) (bool, error) {
	recorder.updates = append(recorder.updates, update)
	return true, nil
}

func TestWorkerCallbackAttributesClaimsAndPreservesOriginIdentityOnRecovery(t *testing.T) {
	current := AgentIdentity{WorkerID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist", MachineID: "a69b7f51-58b9-4f0e-aef3-1fbc23f79827"}
	origin := AgentIdentity{WorkerID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79826", AgentKey: "generalist", MachineID: "b69b7f51-58b9-4f0e-aef3-1fbc23f79827"}
	recorder := &taskCallbackRecorder{}
	callback := identityTaskCallback{inner: recorder, identity: current}
	if _, err := callback.Update(context.Background(), "task", TaskUpdate{Status: "running", RunID: "run", ProgressStep: "planning"}); err != nil {
		t.Fatal(err)
	}
	if _, err := callback.Update(context.Background(), "task", TaskUpdate{Status: "completed", RunID: "run", ExecutionIdentity: &origin}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.updates) != 2 {
		t.Fatalf("recorded %d updates, want 2", len(recorder.updates))
	}
	claim := recorder.updates[0]
	if claim.WorkerID != current.WorkerID || claim.AgentKey != current.AgentKey || claim.MachineID != current.MachineID {
		t.Fatalf("claim identity = %#v, want current worker %#v", claim, current)
	}
	completion := recorder.updates[1]
	if completion.WorkerID != current.WorkerID || completion.ExecutionIdentity == nil || *completion.ExecutionIdentity != origin {
		t.Fatalf("recovery completion must report current claimant and preserve origin: %#v", completion)
	}
}

func TestHTTPCallbackHeartbeatSendsOnlyWorkerLivenessMetadata(t *testing.T) {
	t.Parallel()

	type receivedHeartbeat struct {
		AgentHeartbeat
		Unexpected map[string]json.RawMessage
	}
	var received receivedHeartbeat
	identity, instanceID := newTestMachineIdentity(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", request.Method)
		}
		if request.URL.Path != "/api/internal/automation/agents/heartbeat" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		bodyBytes, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		assertSignedCallbackRequest(t, request, bodyBytes, identity, instanceID)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		if err := json.Unmarshal(mustMarshal(t, body), &received.AgentHeartbeat); err != nil {
			t.Fatalf("decode liveness metadata: %v", err)
		}
		delete(body, "worker_id")
		delete(body, "agent_key")
		delete(body, "machine_id")
		delete(body, "provider")
		delete(body, "model")
		delete(body, "concurrency")
		delete(body, "protocols")
		delete(body, "started_at")
		delete(body, "workspace_readiness")
		received.Unexpected = body
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	heartbeat := AgentHeartbeat{
		WorkerID:    "a69b7f51-58b9-4f0e-aef3-1fbc23f79826",
		AgentKey:    "generalist",
		MachineID:   "a69b7f51-58b9-4f0e-aef3-1fbc23f79827",
		Provider:    "minimax",
		Model:       "MiniMax-M3",
		Concurrency: 2,
		Protocols:   []string{agentprotocol.ProtocolDeliveryPlanStepsV1},
		StartedAt:   "2026-08-09T12:00:00Z",
		WorkspaceReadiness: []WorkspaceReadiness{{
			ID: "dashboard", Ready: true, QAReady: true, VisualQAReady: true,
			ValidationCommandCount: 2, QACommandCount: 1,
		}},
	}
	if err := callback.Heartbeat(context.Background(), heartbeat); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !reflect.DeepEqual(received.AgentHeartbeat, heartbeat) {
		t.Fatalf("heartbeat = %#v, want %#v", received.AgentHeartbeat, heartbeat)
	}
	if len(received.Unexpected) != 0 {
		t.Fatalf("heartbeat exposed unexpected fields: %v", received.Unexpected)
	}
}

func TestHTTPCallbackHeartbeatRejectionExposesStatusButNotResponseBody(t *testing.T) {
	t.Parallel()

	const responseCanary = "PRIVATE_SERVER_DIAGNOSTIC_SHOULD_NOT_ESCAPE"
	identity, instanceID := newTestMachineIdentity(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, responseCanary)
	}))
	defer server.Close()

	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	err := callback.Heartbeat(context.Background(), AgentHeartbeat{
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: identity.MachineID(),
	})
	var rejection *HeartbeatRejectionError
	if !errors.As(err, &rejection) || rejection.StatusCode != http.StatusForbidden {
		t.Fatalf("heartbeat rejection = %#v, want typed HTTP 403: %v", rejection, err)
	}
	if strings.Contains(err.Error(), responseCanary) {
		t.Fatal("heartbeat error exposed the server response body")
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test body: %v", err)
	}
	return body
}
