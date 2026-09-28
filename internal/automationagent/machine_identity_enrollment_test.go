package automationagent

import (
	"testing"

	"events-stocks/internal/agentcallbackauth"
	"github.com/gofrs/uuid"
)

func TestMachineIdentityStoresAndSignsAutomaticEnrollment(t *testing.T) {
	directory := t.TempDir()
	identity, err := LoadLocalMachineIdentity("", directory)
	if err != nil {
		t.Fatal(err)
	}
	if instanceID, err := identity.RegisteredAgentInstanceID("generalist"); err != nil || instanceID != "" {
		t.Fatalf("new machine instance ID = %q, err=%v; want unset", instanceID, err)
	}
	publicKey, signature, err := identity.AgentInstanceEnrollmentProof("generalist")
	if err != nil {
		t.Fatal(err)
	}
	decodedKey, err := agentcallbackauth.DecodePublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	decodedSignature, err := agentcallbackauth.DecodeSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentcallbackauth.VerifyAgentInstanceEnrollment(decodedKey, decodedSignature, "generalist", identity.MachineID(), publicKey); err != nil {
		t.Fatalf("generated proof did not verify: %v", err)
	}
	firstID := uuid.Must(uuid.NewV4()).String()
	if err := identity.StoreRegisteredAgentInstanceID("generalist", firstID); err != nil {
		t.Fatal(err)
	}
	if loadedID, err := identity.RegisteredAgentInstanceID("generalist"); err != nil || loadedID != firstID {
		t.Fatalf("stored instance ID = %q, err=%v; want %q", loadedID, err, firstID)
	}
	secondID := uuid.Must(uuid.NewV4()).String()
	if err := identity.StoreRegisteredAgentInstanceID("generalist", secondID); err != nil {
		t.Fatal(err)
	}
	if loadedID, err := identity.RegisteredAgentInstanceID("generalist"); err != nil || loadedID != secondID {
		t.Fatalf("rotated instance ID = %q, err=%v; want %q", loadedID, err, secondID)
	}
	if _, err := identity.RegisteredAgentInstanceID("../generalist"); err == nil {
		t.Fatal("agent key path traversal was accepted")
	}
	if err := identity.StoreRegisteredAgentInstanceID("generalist", "not-a-uuid"); err == nil {
		t.Fatal("invalid control-plane instance ID was persisted")
	}
}
