package automationagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"events-stocks/internal/agentcallbackauth"
	"github.com/gofrs/uuid"
)

func newTestMachineIdentity(t *testing.T) (MachineIdentity, string) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return MachineIdentity{machineID: uuid.Must(uuid.NewV4()).String(), privateKey: privateKey}, uuid.Must(uuid.NewV4()).String()
}

func newTestHTTPCallback(t *testing.T, server *httptest.Server) *HTTPCallback {
	t.Helper()
	identity, instanceID := newTestMachineIdentity(t)
	callback, err := NewHTTPCallback(server.URL, identity, instanceID, server.Client())
	if err != nil {
		t.Fatalf("NewHTTPCallback: %v", err)
	}
	return callback
}

func assertSignedCallbackRequest(t *testing.T, request *http.Request, body []byte, identity MachineIdentity, instanceID string) {
	t.Helper()
	if request.Header.Get("X-Automation-Secret") != "" {
		t.Fatal("callback sent the legacy shared secret")
	}
	timestamp, err := strconv.ParseInt(request.Header.Get(agentcallbackauth.TimestampHeader), 10, 64)
	if err != nil {
		t.Fatalf("callback timestamp is invalid: %v", err)
	}
	signature, err := agentcallbackauth.DecodeSignature(request.Header.Get(agentcallbackauth.SignatureHeader))
	if err != nil {
		t.Fatalf("callback signature is invalid: %v", err)
	}
	nonce := request.Header.Get(agentcallbackauth.NonceHeader)
	if request.Header.Get(agentcallbackauth.InstanceIDHeader) != instanceID {
		t.Fatal("callback used the wrong registered instance ID")
	}
	if err := agentcallbackauth.VerifyRequest(identity.PublicKey(), signature, instanceID, request.Method, request.URL.RequestURI(), timestamp, nonce, body, time.Now().UTC()); err != nil {
		t.Fatalf("callback signature verification failed: %v", err)
	}
}

func newTestCallbackWithIdentity(t *testing.T, server *httptest.Server, identity MachineIdentity, instanceID string) *HTTPCallback {
	t.Helper()
	callback, err := NewHTTPCallback(server.URL, identity, instanceID, server.Client())
	if err != nil {
		t.Fatalf("NewHTTPCallback: %v", err)
	}
	return callback
}
