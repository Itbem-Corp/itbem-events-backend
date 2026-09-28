package agentcallbackauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestSignAndVerifyRequestBindsEveryFieldAndRawBody(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.Must(uuid.NewV4()).String()
	nonce := uuid.Must(uuid.NewV4()).String()
	now := time.Unix(1_800_000_000, 0).UTC()
	body := []byte("{\"worker_id\":\"worker-1\"}\n")
	signature, err := SignRequest(privateKey, instanceID, "put", "/api/internal/automation/agents/heartbeat?draining=false", now.Unix(), nonce, body)
	if err != nil {
		t.Fatalf("SignRequest() error = %v", err)
	}
	if err := VerifyRequest(publicKey, signature, instanceID, "PUT", "/api/internal/automation/agents/heartbeat?draining=false", now.Unix(), nonce, body, now); err != nil {
		t.Fatalf("VerifyRequest() rejected the matching request: %v", err)
	}
	for _, test := range []struct {
		name      string
		instance  string
		method    string
		uri       string
		timestamp int64
		nonce     string
		body      []byte
	}{
		{name: "instance", instance: uuid.Must(uuid.NewV4()).String(), method: "PUT", uri: "/api/internal/automation/agents/heartbeat?draining=false", timestamp: now.Unix(), nonce: nonce, body: body},
		{name: "method", instance: instanceID, method: "POST", uri: "/api/internal/automation/agents/heartbeat?draining=false", timestamp: now.Unix(), nonce: nonce, body: body},
		{name: "path and query", instance: instanceID, method: "PUT", uri: "/api/internal/automation/agents/heartbeat?draining=true", timestamp: now.Unix(), nonce: nonce, body: body},
		{name: "timestamp", instance: instanceID, method: "PUT", uri: "/api/internal/automation/agents/heartbeat?draining=false", timestamp: now.Unix() + 1, nonce: nonce, body: body},
		{name: "nonce", instance: instanceID, method: "PUT", uri: "/api/internal/automation/agents/heartbeat?draining=false", timestamp: now.Unix(), nonce: uuid.Must(uuid.NewV4()).String(), body: body},
		{name: "raw body", instance: instanceID, method: "PUT", uri: "/api/internal/automation/agents/heartbeat?draining=false", timestamp: now.Unix(), nonce: nonce, body: []byte(strings.TrimSpace(string(body)))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := VerifyRequest(publicKey, signature, test.instance, test.method, test.uri, test.timestamp, test.nonce, test.body, now); err == nil {
				t.Fatal("altered request was accepted")
			}
		})
	}
}

func TestRequestSignatureRejectsExpiredFutureAndMalformedIdentity(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	instanceID := uuid.Must(uuid.NewV4()).String()
	nonce := uuid.Must(uuid.NewV4()).String()
	body := []byte("{}")
	for _, timestamp := range []int64{now.Add(-MaxClockSkew - time.Second).Unix(), now.Add(MaxClockSkew + time.Second).Unix()} {
		signature, err := SignRequest(privateKey, instanceID, "POST", "/callback", timestamp, nonce, body)
		if err != nil {
			t.Fatalf("SignRequest() error = %v", err)
		}
		if err := VerifyRequest(publicKey, signature, instanceID, "POST", "/callback", timestamp, nonce, body, now); err == nil {
			t.Fatalf("timestamp %d outside the clock window was accepted", timestamp)
		}
	}
	if _, err := SignRequest(privateKey, "not-a-uuid", "POST", "/callback", now.Unix(), nonce, body); err == nil {
		t.Fatal("invalid instance id was accepted")
	}
	if _, err := SignRequest(privateKey, instanceID, "POST", "https://example.test/callback", now.Unix(), nonce, body); err == nil {
		t.Fatal("absolute request URI was accepted")
	}
	if _, err := SignRequest(privateKey, instanceID, "POST", "/callback", now.Unix(), "zero", body); err == nil {
		t.Fatal("invalid nonce was accepted")
	}
}

func TestPublicKeyAndSignatureEncodingsAreCanonicalAndDisplaySafe(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := EncodePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	decodedKey, err := DecodePublicKey(encodedKey)
	if err != nil || string(decodedKey) != string(publicKey) {
		t.Fatalf("public key round trip failed: err=%v", err)
	}
	if _, err := DecodePublicKey(encodedKey + "="); err == nil {
		t.Fatal("padded public key was accepted")
	}
	now := time.Now().UTC().Truncate(time.Second)
	signature, err := SignRequest(privateKey, uuid.Must(uuid.NewV4()).String(), "POST", "/callback", now.Unix(), uuid.Must(uuid.NewV4()).String(), []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}
	encodedSignature, err := EncodeSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	decodedSignature, err := DecodeSignature(encodedSignature)
	if err != nil || string(decodedSignature) != string(signature) {
		t.Fatalf("signature round trip failed: err=%v", err)
	}
	if _, err := DecodeSignature(encodedSignature + "="); err == nil {
		t.Fatal("padded signature was accepted")
	}
	fingerprint, err := PublicKeyFingerprint(publicKey)
	if err != nil || !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected public-key fingerprint %q, err=%v", fingerprint, err)
	}
	if strings.Contains(fingerprint, encodedKey) || strings.Contains(fingerprint, encodedSignature) {
		t.Fatal("fingerprint unexpectedly contains key or signature material")
	}
}

func TestAgentInstanceEnrollmentProofBindsProfileMachineAndPublicKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := EncodePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	machineID := uuid.Must(uuid.NewV4()).String()
	message, err := AgentInstanceEnrollmentMessage("generalist", machineID, encodedKey)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, message)
	if err := VerifyAgentInstanceEnrollment(publicKey, signature, "generalist", machineID, encodedKey); err != nil {
		t.Fatalf("matching enrollment proof rejected: %v", err)
	}
	for _, changed := range []struct {
		name      string
		agentKey  string
		machineID string
		publicKey string
	}{
		{name: "profile", agentKey: "reviewer", machineID: machineID, publicKey: encodedKey},
		{name: "machine", agentKey: "generalist", machineID: uuid.Must(uuid.NewV4()).String(), publicKey: encodedKey},
		{name: "public key", agentKey: "generalist", machineID: machineID, publicKey: encodedKey + "a"},
	} {
		t.Run(changed.name, func(t *testing.T) {
			if err := VerifyAgentInstanceEnrollment(publicKey, signature, changed.agentKey, changed.machineID, changed.publicKey); err == nil {
				t.Fatal("enrollment proof was accepted after a signed field changed")
			}
		})
	}
	if _, err := AgentInstanceEnrollmentMessage("bad\nprofile", machineID, encodedKey); err == nil {
		t.Fatal("newline-containing profile was accepted")
	}
}
