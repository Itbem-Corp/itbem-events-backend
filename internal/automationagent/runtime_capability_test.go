package automationagent

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gofrs/uuid"
)

func TestRuntimeConfigRequiresProviderCapabilities(t *testing.T) {
	values := map[string]string{
		"ITBEM_AI_QUEUE_URL":      "http://127.0.0.1:4566/000000000000/itbem-ai-local",
		"AWS_REGION":              "us-east-1",
		"ITBEM_API_BASE_URL":      "http://127.0.0.1:18080",
		"ITBEM_AGENT_INSTANCE_ID": uuid.Must(uuid.NewV4()).String(),
		"ITBEM_AI_INPUT_BUCKET":   "itbem-ai-inputs-local",
		"ITBEM_AI_OUTPUT_BUCKET":  "itbem-ai-outputs-local",
		"ITBEM_AI_SQS_ENDPOINT":   "http://127.0.0.1:4566",
		"ITBEM_AI_S3_ENDPOINT":    "http://127.0.0.1:4566",
		"ITBEM_AI_STATE_DIR":      t.TempDir(),
	}
	config, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if !config.RequireProviderCapabilities {
		t.Fatal("production runtime workers must require a versioned provider capability contract")
	}
	if config.AgentKey != "generalist" {
		t.Fatalf("default agent key = %q, want generalist", config.AgentKey)
	}
	if parsed, err := uuid.FromString(config.MachineID); err != nil || parsed == uuid.Nil {
		t.Fatalf("runtime must provision a stable opaque machine identity, got %q (%v)", config.MachineID, err)
	}
	persistedConfig, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil || persistedConfig.MachineID != config.MachineID {
		t.Fatalf("runtime reload must preserve the machine identity: first=%q second=%q err=%v", config.MachineID, persistedConfig.MachineID, err)
	}

	values["ITBEM_AI_AGENT_KEY"] = "document-specialist"
	values["ITBEM_AI_MACHINE_ID"] = config.MachineID
	configured, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if configured.AgentKey != "document-specialist" || configured.MachineID != values["ITBEM_AI_MACHINE_ID"] || configured.CallbackIdentity.MachineID() != config.CallbackIdentity.MachineID() || string(configured.CallbackIdentity.PublicKey()) != string(config.CallbackIdentity.PublicKey()) {
		t.Fatalf("explicit worker identity = %#v, want configured values", configured.WorkerConfig)
	}
	values["ITBEM_AI_MACHINE_ID"] = "desktop-workstation"
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("machine identity must reject a hostname-like value")
	}
}

func TestRuntimeConfigRequiresRegisteredAgentInstanceID(t *testing.T) {
	values := map[string]string{
		"ITBEM_AI_QUEUE_URL":     "http://127.0.0.1:4566/000000000000/itbem-ai-local",
		"AWS_REGION":             "us-east-1",
		"ITBEM_API_BASE_URL":     "http://127.0.0.1:18080",
		"ITBEM_AI_INPUT_BUCKET":  "itbem-ai-inputs-local",
		"ITBEM_AI_OUTPUT_BUCKET": "itbem-ai-outputs-local",
		"ITBEM_AI_STATE_DIR":     t.TempDir(),
	}
	if _, err := LoadRuntimeConfig(func(name string) string { return values[name] }); err == nil || !strings.Contains(err.Error(), "--ensure-registered") {
		t.Fatalf("runtime without registered instance ID must fail closed, got %v", err)
	}
}

func TestMachineIdentityIsStableAcrossConcurrentProcessesAndDistinctAcrossMachines(t *testing.T) {
	firstDirectory := t.TempDir()
	const contenders = 12
	type identityResult struct {
		identity MachineIdentity
		err      error
	}
	identities := make(chan identityResult, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			identity, err := loadOrCreateMachineIdentity(firstDirectory, "")
			identities <- identityResult{identity: identity, err: err}
		}()
	}
	group.Wait()
	close(identities)
	var first MachineIdentity
	for result := range identities {
		if result.err != nil {
			t.Fatalf("concurrent identity creation failed: %v", result.err)
		}
		if first.machineID == "" {
			first = result.identity
			continue
		}
		if result.identity.machineID != first.machineID || string(result.identity.privateKey) != string(first.privateKey) {
			t.Fatalf("concurrent processes selected mismatched machine identities or signing keys")
		}
	}
	if first.machineID == "" {
		t.Fatal("concurrent identity creation returned no IDs")
	}
	restarted, err := loadOrCreateMachineIdentity(firstDirectory, "")
	if err != nil || restarted.machineID != first.machineID || string(restarted.privateKey) != string(first.privateKey) {
		t.Fatalf("restart identity or key changed: err=%v", err)
	}
	secondMachine, err := loadOrCreateMachineIdentity(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if secondMachine.machineID == first.machineID || string(secondMachine.PublicKey()) == string(first.PublicKey()) {
		t.Fatalf("different local state directories reused machine identity")
	}
	if parsed, err := uuid.FromString(secondMachine.machineID); err != nil || parsed == uuid.Nil {
		t.Fatalf("second machine ID is not an opaque UUID: %q (%v)", secondMachine.machineID, err)
	}
}

func TestMachineIdentityRotationRequiresNewRegistrationMaterial(t *testing.T) {
	directory := t.TempDir()
	original, err := LoadLocalMachineIdentity("", directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, machineIdentityFileName)); err != nil {
		t.Fatal(err)
	}
	rotated, err := LoadLocalMachineIdentity(original.MachineID(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.MachineID() != original.MachineID() || string(rotated.PublicKey()) == string(original.PublicKey()) {
		t.Fatal("re-provisioning with the registered machine ID must rotate only the signing key")
	}
}

func TestMachineSigningKeyIsProtectedAndPersistsAcrossReload(t *testing.T) {
	directory := t.TempDir()
	identity, err := LoadLocalMachineIdentity("", directory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, machineIdentityFileName)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		cleartext := []byte(base64.RawStdEncoding.EncodeToString(identity.privateKey))
		if bytes.Contains(contents, cleartext) {
			t.Fatal("Windows identity file persisted the private key without DPAPI protection")
		}
	} else {
		directoryInfo, dirErr := os.Stat(directory)
		fileInfo, fileErr := os.Stat(path)
		if dirErr != nil || fileErr != nil || directoryInfo.Mode().Perm()&0o077 != 0 || fileInfo.Mode().Perm()&0o077 != 0 {
			t.Fatalf("Unix identity storage is not private: dir=%v file=%v", dirErr, fileErr)
		}
	}
	reloaded, err := LoadLocalMachineIdentity("", directory)
	if err != nil || string(reloaded.privateKey) != string(identity.privateKey) {
		t.Fatalf("local signing key did not survive a restart: %v", err)
	}
	protected, err := protectMachineIdentityKey(identity.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	unprotected, err := unprotectMachineIdentityKey(protected)
	if err != nil || !bytes.Equal(unprotected, identity.privateKey) {
		t.Fatalf("platform key protection did not round-trip: %v", err)
	}
}

func TestMachineIdentityFailsClosedForUnwritableOrInvalidStorage(t *testing.T) {
	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("ordinary file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateMachineID(notDirectory); err == nil {
		t.Fatal("identity creation must fail closed when state directory cannot be created")
	}
	invalidDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(invalidDirectory, machineIdentityFileName), []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateMachineID(invalidDirectory); err == nil || !strings.Contains(err.Error(), "identity is invalid") {
		t.Fatalf("invalid persisted identity must fail closed, got %v", err)
	}
}

func TestConfiguredMachineIdentityIsValidatedAndPreserved(t *testing.T) {
	expected := uuid.Must(uuid.NewV4())
	directory := t.TempDir()
	identity, err := LoadLocalMachineIdentity(expected.String(), directory)
	if err != nil || identity.MachineID() != expected.String() {
		t.Fatalf("configured ID=%q err=%v; want %q", identity.MachineID(), err, expected.String())
	}
	loaded, err := LoadLocalMachineIdentity(expected.String(), directory)
	if err != nil || string(loaded.PublicKey()) != string(identity.PublicKey()) {
		t.Fatalf("configured identity did not preserve its signing key: err=%v", err)
	}
	if _, err := LoadLocalMachineIdentity(uuid.Must(uuid.NewV4()).String(), directory); err == nil {
		t.Fatal("configured ID must not silently replace an already registered local identity")
	}
	if _, err := LoadLocalMachineIdentity("desktop-workstation", t.TempDir()); err == nil {
		t.Fatal("configured hostname-like machine identity must be rejected")
	}
	if _, err := LoadLocalMachineIdentity("", "relative-state-path"); err == nil {
		t.Fatal("relative state directory must be rejected")
	}
}
