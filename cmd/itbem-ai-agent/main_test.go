package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentprotocol"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"github.com/gofrs/uuid"
)

func TestRuntimeAnnouncesOnlySupportedPlanStepProtocol(t *testing.T) {
	protocols := supportedRuntimeProtocols()
	if len(protocols) != 1 || protocols[0] != agentprotocol.ProtocolDeliveryPlanStepsV1 {
		t.Fatalf("runtime protocols = %#v, want only %q", protocols, agentprotocol.ProtocolDeliveryPlanStepsV1)
	}
}

func TestDoctorReportFailsClosedWithoutExecutionDependenciesAndNeverLeaksValues(t *testing.T) {
	report, ready, err := doctorReport(func(key string) string {
		if key == "AUTOMATION_CALLBACK_SECRET" {
			return "must-never-appear"
		}
		if key == "ITBEM_AI_GATEWAY_URL" {
			return "https://gateway.example.test/api/internal/automation/inference"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready || report["ready"] != false || report["workspaces_ready"] != false {
		t.Fatalf("missing workspace/runtime configuration must fail closed: %#v", report)
	}
	provider, ok := report["provider"].(map[string]any)
	if !ok || provider["ready"] != true || provider["status"] != "configured_unverified" {
		t.Fatalf("a configured provider should be reported without exposing its key: %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-never-appear") {
		t.Fatalf("doctor output leaked a configured secret: %s", raw)
	}
}

func TestMachineIdentityEnrollmentReportPersistsAndShowsOnlyPublicMaterial(t *testing.T) {
	stateDirectory := t.TempDir()
	lookup := func(key string) string {
		if key == "ITBEM_AI_STATE_DIR" {
			return stateDirectory
		}
		return ""
	}
	first, err := machineIdentityReport(lookup)
	if err != nil {
		t.Fatal(err)
	}
	second, err := machineIdentityReport(lookup)
	if err != nil {
		t.Fatal(err)
	}
	if first["machine_id"] == "" || first["machine_id"] != second["machine_id"] || first["public_key"] == "" || first["public_key"] != second["public_key"] {
		t.Fatalf("enrollment identity did not persist: first=%v second=%v", first, second)
	}
	if _, err := agentcallbackauth.DecodePublicKey(first["public_key"]); err != nil {
		t.Fatalf("public key is not canonical base64url: %v", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), filepath.Base(stateDirectory)) {
		t.Fatalf("enrollment output exposed non-public material: %s", encoded)
	}
}

func TestDoctorReviewIngressNeverLeaksConfigurationAndReportsIncompleteSetup(t *testing.T) {
	status := doctorReviewIngress(func(key string) string {
		if key == "GITHUB_REVIEW_WEBHOOK_SECRET" {
			return "must-never-appear"
		}
		if key == "GITHUB_REVIEW_REPOSITORIES" {
			return "itbem/backend"
		}
		return ""
	}, false, true)
	if status["enabled"] != true || status["ready"] != false || status["status"] != "incomplete" || status["allowed_repository_count"] != 1 {
		t.Fatalf("incomplete ingress should be explicit: %#v", status)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "must-never-appear") || strings.Contains(string(raw), "itbem/backend") {
		t.Fatalf("doctor review ingress leaked secret or repository identity: %s", raw)
	}
}

func TestDoctorReportSurfacesSandboxEvidenceWithoutMakingItAReadinessGrant(t *testing.T) {
	report, ready, err := doctorReport(func(key string) string {
		switch key {
		case "ITBEM_AI_SANDBOX_ATTESTATION_JSON":
			return `{"runtime":"firecracker","runtime_version":"1.7.0","transport":"virtio_vsock","evidence_scope":"synthetic_guest_fixture","guest_command_verified":true}`
		case "AUTOMATION_CALLBACK_SECRET":
			return "configured-but-never-serialized"
		case "ITBEM_AI_GATEWAY_URL":
			return "https://gateway.example.test/api/internal/automation/inference"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("sandbox evidence must not make an otherwise unready doctor report ready")
	}
	evidence, ok := report["sandbox_attestation"].(map[string]any)
	if !ok || evidence["ready"] != true || evidence["status"] != "verified_evidence" || evidence["runtime"] != "firecracker" || evidence["transport"] != "virtio_vsock" {
		t.Fatalf("doctor report did not expose validated evidence: %#v", report)
	}
	if !strings.Contains(evidence["message"].(string), "observational only") {
		t.Fatalf("doctor report must preserve the non-authorizing boundary: %#v", evidence)
	}
}

func TestGitHubAuthProbeRequiresPublicationOrRegisteredGitHubSourceAndRedactsFailures(t *testing.T) {
	engineer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}}
	called := false
	lookedUp := make([]string, 0, 4)
	report, err := githubAuthProbeReport(context.Background(), engineer, func(name string) string {
		lookedUp = append(lookedUp, name)
		return ""
	}, func(context.Context, automationagent.GitHubAppConfig) error { called = true; return nil })
	if err != nil || called || report["ready"] != true || report["status"] != "not_required" || report["network_checks_made"] != false {
		t.Fatalf("non-publishing GitHub probe = %#v, called=%v, err=%v", report, called, err)
	}
	for _, name := range lookedUp {
		if strings.HasPrefix(name, "ITBEM_GITHUB_APP_") || strings.HasPrefix(name, "ITBEM_GITHUB_INSTALLATION_") {
			t.Fatalf("non-publishing, unregistered engineer probe read publication credential %q", name)
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	reviewer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview}}
	lookup := func(name string) string {
		values := map[string]string{"ITBEM_GITHUB_APP_ID": "12345", "ITBEM_GITHUB_INSTALLATION_IDS": "67890,67891", "ITBEM_GITHUB_APP_PRIVATE_KEY": privatePEM}
		return values[name]
	}
	verified := make([]string, 0, 2)
	report, err = githubAuthProbeReport(context.Background(), reviewer, lookup, func(_ context.Context, config automationagent.GitHubAppConfig) error {
		called = true
		if config.AppID != "12345" {
			t.Fatalf("unexpected GitHub App identity: %#v", config)
		}
		verified = append(verified, config.InstallationID)
		return nil
	})
	if err != nil || !called || report["ready"] != true || report["status"] != "authenticated" || report["installation_count"] != 2 || strings.Join(verified, ",") != "67890,67891" {
		t.Fatalf("reviewer GitHub probe = %#v, installations=%v, err=%v", report, verified, err)
	}
	_, err = githubAuthProbeReport(context.Background(), reviewer, lookup, func(context.Context, automationagent.GitHubAppConfig) error { return fmt.Errorf("must-never-appear") })
	if err == nil || strings.Contains(err.Error(), "must-never-appear") {
		t.Fatalf("GitHub probe did not redact remote failure: %v", err)
	}
}

func TestProviderResolutionRequiresGatewayInEveryEnvironment(t *testing.T) {
	lookup := func(key string) string {
		switch key {
		case "MINIMAX_API_KEY":
			return "local-key-must-not-be-used"
		default:
			return ""
		}
	}
	if _, err := loadExecutionProvider(lookup); err == nil || !strings.Contains(err.Error(), "ITBEM_AI_GATEWAY_URL") {
		t.Fatalf("agent accepted a direct provider key: %v", err)
	}
}

func TestQueueDoesNotStartUntilSignedEnrollmentHeartbeatIsAccepted(t *testing.T) {
	for _, test := range []struct {
		name       string
		statusCode int
		wantQueue  bool
	}{
		{name: "bad heartbeat", statusCode: http.StatusBadRequest, wantQueue: false},
		{name: "unknown or revoked registration", statusCode: http.StatusUnauthorized, wantQueue: false},
		{name: "mismatched profile", statusCode: http.StatusForbidden, wantQueue: false},
		{name: "unknown route", statusCode: http.StatusNotFound, wantQueue: false},
		{name: "accepted registration", statusCode: http.StatusOK, wantQueue: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/api/internal/automation/agents/heartbeat" {
					t.Errorf("unexpected preflight request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get(agentcallbackauth.InstanceIDHeader) == "" || r.Header.Get(agentcallbackauth.SignatureHeader) == "" {
					t.Error("preflight heartbeat must use the registered machine signature")
				}
				var received automationagent.AgentHeartbeat
				if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&received); err != nil {
					t.Errorf("decode preflight heartbeat: %v", err)
				} else if len(received.Protocols) != 1 || received.Protocols[0] != agentprotocol.ProtocolDeliveryPlanStepsV1 {
					t.Errorf("preflight heartbeat protocols = %#v, want only %q", received.Protocols, agentprotocol.ProtocolDeliveryPlanStepsV1)
				}
				w.WriteHeader(test.statusCode)
			}))
			defer server.Close()

			identity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			callback, err := automationagent.NewHTTPCallback(server.URL, identity, uuid.Must(uuid.NewV4()).String(), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			heartbeat := automationagent.AgentHeartbeat{
				WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: identity.MachineID(),
				Provider: "openai", Model: "test-model", Concurrency: 1, Protocols: supportedRuntimeProtocols(), StartedAt: time.Now().UTC().Format(time.RFC3339),
			}
			queueStarted := false
			err = runQueueAfterEnrollmentWithWait(context.Background(), callback, heartbeat, func(string) string { return "" }, func() error {
				queueStarted = true
				return nil
			}, func(_ context.Context, _ time.Duration) error { return nil })
			if test.wantQueue && err != nil {
				t.Fatalf("accepted registration should proceed to queue: %v", err)
			}
			if !test.wantQueue && err == nil {
				t.Fatal("rejected registration should prevent queue polling")
			}
			if queueStarted != test.wantQueue {
				t.Fatalf("queueStarted = %t, want %t", queueStarted, test.wantQueue)
			}
		})
	}
}

func TestTransientEnrollmentHeartbeatFailureRetriesBeforeStartingQueue(t *testing.T) {
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/api/internal/automation/agents/heartbeat" {
					t.Errorf("unexpected enrollment request: %s %s", r.Method, r.URL.Path)
				}
				if requests.Add(1) == 1 {
					w.WriteHeader(statusCode)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			identity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			callback, err := automationagent.NewHTTPCallback(server.URL, identity, uuid.Must(uuid.NewV4()).String(), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			heartbeat := automationagent.AgentHeartbeat{
				WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: identity.MachineID(),
				Provider: "openai", Model: "test-model", Concurrency: 1, StartedAt: time.Now().UTC().Format(time.RFC3339),
			}
			queueStarted := false
			var retryDelays []time.Duration
			err = runQueueAfterEnrollmentWithWait(context.Background(), callback, heartbeat, func(string) string { return "" }, func() error {
				queueStarted = true
				return nil
			}, func(_ context.Context, delay time.Duration) error {
				retryDelays = append(retryDelays, delay)
				return nil
			})
			if err != nil {
				t.Fatalf("transient heartbeat followed by acceptance should start queue: %v", err)
			}
			if !queueStarted || requests.Load() != 2 || len(retryDelays) != 1 || retryDelays[0] != time.Second {
				t.Fatalf("queueStarted=%t requests=%d retryDelays=%v; want queue after one bounded retry", queueStarted, requests.Load(), retryDelays)
			}
		})
	}
}

func TestEnrollmentNetworkFailureRetriesBeforeStartingQueue(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("httptest response writer does not support a local connection close")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack local test connection: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	identity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	callback, err := automationagent.NewHTTPCallback(server.URL, identity, uuid.Must(uuid.NewV4()).String(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := automationagent.AgentHeartbeat{
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: identity.MachineID(),
		Provider: "openai", Model: "test-model", Concurrency: 1, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	queueStarted := false
	err = runQueueAfterEnrollmentWithWait(context.Background(), callback, heartbeat, func(string) string { return "" }, func() error {
		queueStarted = true
		return nil
	}, func(_ context.Context, _ time.Duration) error { return nil })
	if err != nil || !queueStarted || requests.Load() != 2 {
		t.Fatalf("temporary network error should be retried before queue start: err=%v queueStarted=%t requests=%d", err, queueStarted, requests.Load())
	}
}

func TestPeriodicTerminalHeartbeatRejectionInvokesDrainStopPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("private server detail must not be logged"))
	}))
	defer server.Close()

	identity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	callback, err := automationagent.NewHTTPCallback(server.URL, identity, uuid.Must(uuid.NewV4()).String(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := automationagent.AgentHeartbeat{
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: identity.MachineID(),
		Provider: "openai", Model: "test-model", Concurrency: 1, StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopPath := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		reportHeartbeatsAtInterval(ctx, callback, heartbeat, nil, func(string) string { return "" }, time.Millisecond, func() {
			close(stopPath)
		})
	}()
	select {
	case <-stopPath:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal identity rejection did not invoke the queue drain/stop path")
	}
	cancel()
	select {
	case <-monitorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat monitor did not stop after terminal rejection")
	}
}

func TestPeriodicTransientHeartbeatFailureDoesNotInvokeDrainStopPath(t *testing.T) {
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var requests atomic.Int32
			secondHeartbeat := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.WriteHeader(statusCode)
					return
				}
				select {
				case <-secondHeartbeat:
				default:
					close(secondHeartbeat)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			identity, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			callback, err := automationagent.NewHTTPCallback(server.URL, identity, uuid.Must(uuid.NewV4()).String(), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			heartbeat := automationagent.AgentHeartbeat{
				WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: identity.MachineID(),
				Provider: "openai", Model: "test-model", Concurrency: 1, StartedAt: time.Now().UTC().Format(time.RFC3339),
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stopCalled atomic.Bool
			monitorDone := make(chan struct{})
			go func() {
				defer close(monitorDone)
				reportHeartbeatsAtInterval(ctx, callback, heartbeat, nil, func(string) string { return "" }, time.Millisecond, func() {
					stopCalled.Store(true)
				})
			}()
			select {
			case <-secondHeartbeat:
			case <-time.After(2 * time.Second):
				t.Fatal("heartbeat monitor did not retry after transient status")
			}
			cancel()
			select {
			case <-monitorDone:
			case <-time.After(2 * time.Second):
				t.Fatal("heartbeat monitor did not stop after cancellation")
			}
			if stopCalled.Load() {
				t.Fatal("transient heartbeat failure incorrectly invoked the queue drain/stop path")
			}
		})
	}
}
