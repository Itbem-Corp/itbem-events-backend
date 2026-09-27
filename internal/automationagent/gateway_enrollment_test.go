package automationagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentwork"
	"github.com/gofrs/uuid"
)

func TestHTTPGatewayEnrollsMachineAndAcceptsServerIssuedInstanceID(t *testing.T) {
	identity, err := LoadLocalMachineIdentity("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wantID := uuid.Must(uuid.NewV4()).String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != gatewayAgentInstanceEnrollmentPath {
			t.Fatalf("enrollment request target = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Agent-Gateway-Token") != "lane-token" || r.Header.Get("X-Agent-Role") != string(agentwork.RoleReviewer) || r.Header.Get("X-Agent-Lane") != string(agentwork.LaneReview) {
			t.Fatal("enrollment request did not use the lane-bound gateway identity")
		}
		var request gatewayAgentInstanceEnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.AgentKey != "generalist" || request.MachineID != identity.MachineID() {
			t.Fatalf("unexpected enrollment identity: %#v", request)
		}
		publicKey, err := agentcallbackauth.DecodePublicKey(request.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		signature, err := agentcallbackauth.DecodeSignature(request.Signature)
		if err != nil {
			t.Fatal(err)
		}
		if err := agentcallbackauth.VerifyAgentInstanceEnrollment(publicKey, signature, request.AgentKey, request.MachineID, request.PublicKey); err != nil {
			t.Fatalf("machine proof was invalid: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"message":"registered","data":{"instance":{"id":"` + wantID + `"}}}`))
	}))
	defer server.Close()

	gateway, err := NewHTTPGateway(server.URL, "lane-token", agentwork.RoleReviewer, agentwork.LaneReview, nil)
	if err != nil {
		t.Fatal(err)
	}
	instanceID, err := gateway.EnrollAgentInstance(context.Background(), "generalist", identity)
	if err != nil || instanceID != wantID {
		t.Fatalf("enrollment returned %q, %v; want %q", instanceID, err, wantID)
	}
}

func TestHTTPGatewayDoesNotFollowEnrollmentRedirects(t *testing.T) {
	identity, err := LoadLocalMachineIdentity("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	gateway, err := NewHTTPGateway(origin.URL, "lane-token", agentwork.RoleReviewer, agentwork.LaneReview, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.EnrollAgentInstance(context.Background(), "generalist", identity); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect response error = %v; want rejected 302", err)
	}
	if redirected {
		t.Fatal("gateway token was forwarded to an enrollment redirect target")
	}
}

func TestEnsureGatewayAgentInstancePersistsIssuedIDForNextPreflight(t *testing.T) {
	stateDirectory := t.TempDir()
	wantID := uuid.Must(uuid.NewV4()).String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request gatewayAgentInstanceEnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		publicKey, err := agentcallbackauth.DecodePublicKey(request.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		signature, err := agentcallbackauth.DecodeSignature(request.Signature)
		if err != nil {
			t.Fatal(err)
		}
		if err := agentcallbackauth.VerifyAgentInstanceEnrollment(publicKey, signature, request.AgentKey, request.MachineID, request.PublicKey); err != nil {
			t.Fatalf("invalid machine proof: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"data":{"instance":{"id":"` + wantID + `"}}}`))
	}))
	defer server.Close()

	values := map[string]string{
		"ITBEM_AI_TRANSPORT":      "gateway",
		"ITBEM_AI_GATEWAY_TOKEN":  "lane-token",
		"ITBEM_AI_ROLE":           "reviewer",
		"ITBEM_AI_QUEUE_LANE":     "review",
		"ITBEM_AI_AGENT_KEY":      "generalist",
		"ITBEM_AI_STATE_DIR":      stateDirectory,
		"ITBEM_API_BASE_URL":      server.URL,
		"ITBEM_AI_INPUT_BUCKET":   "itbem-ai-inputs-test",
		"ITBEM_AI_OUTPUT_BUCKET":  "itbem-ai-outputs-test",
		"ITBEM_AGENT_INSTANCE_ID": "",
	}
	lookup := func(name string) string { return values[name] }
	instanceID, err := EnsureGatewayAgentInstance(context.Background(), lookup)
	if err != nil || instanceID != wantID {
		t.Fatalf("EnsureGatewayAgentInstance() = %q, %v; want %q", instanceID, err, wantID)
	}
	config, err := LoadRuntimeConfig(lookup)
	if err != nil || config.AgentInstanceID != wantID {
		t.Fatalf("runtime after enrollment has ID %q, err=%v; want %q", config.AgentInstanceID, err, wantID)
	}
}

func TestEnsureGatewayAgentInstanceLeavesAWSMigrationTransportAlone(t *testing.T) {
	instanceID, err := EnsureGatewayAgentInstance(context.Background(), func(name string) string {
		if name == "ITBEM_AI_QUEUE_URL" {
			return "https://sqs.example/queue"
		}
		return ""
	})
	if err != nil || instanceID != "" {
		t.Fatalf("AWS migration enrollment = %q, %v; want no-op", instanceID, err)
	}
}
