package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHTTPCallbackRetainsOnlyAnExplicitExpiredReservationConflict(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set(retryReservationHeader, "1")
		writer.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()

	callback, err := NewHTTPCallback(server.URL, "test-secret", server.Client())
	if err != nil {
		t.Fatalf("NewHTTPCallback: %v", err)
	}
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

	callback, err := NewHTTPCallback(server.URL, "test-secret", server.Client())
	if err != nil {
		t.Fatalf("NewHTTPCallback: %v", err)
	}
	updated, err := callback.Update(context.Background(), "d4a4b837-2e18-43af-9f58-6d59629db2bb", TaskUpdate{Status: "completed"})
	if updated || err != nil {
		t.Fatalf("ordinary callback conflict = updated:%v err:%v, want ignored", updated, err)
	}
}

func TestHTTPCallbackHeartbeatSendsOnlyWorkerLivenessMetadata(t *testing.T) {
	t.Parallel()

	type receivedHeartbeat struct {
		AgentHeartbeat
		Unexpected map[string]json.RawMessage
	}
	var received receivedHeartbeat
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", request.Method)
		}
		if request.URL.Path != "/api/internal/automation/agents/heartbeat" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("X-Automation-Secret") != "test-secret" {
			t.Fatal("heartbeat callback did not authenticate")
		}

		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode heartbeat: %v", err)
		}
		if err := json.Unmarshal(mustMarshal(t, body), &received.AgentHeartbeat); err != nil {
			t.Fatalf("decode liveness metadata: %v", err)
		}
		delete(body, "worker_id")
		delete(body, "provider")
		delete(body, "model")
		delete(body, "concurrency")
		delete(body, "started_at")
		delete(body, "workspace_readiness")
		received.Unexpected = body
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	callback, err := NewHTTPCallback(server.URL, "test-secret", server.Client())
	if err != nil {
		t.Fatalf("NewHTTPCallback: %v", err)
	}
	heartbeat := AgentHeartbeat{
		WorkerID:    "a69b7f51-58b9-4f0e-aef3-1fbc23f79826",
		Provider:    "minimax",
		Model:       "MiniMax-M3",
		Concurrency: 2,
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

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test body: %v", err)
	}
	return body
}
