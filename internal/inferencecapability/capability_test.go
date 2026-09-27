package inferencecapability

import (
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

const testSigningKey = "unit-test-server-only-signing-key-with-at-least-thirty-two-bytes"
const testCallbackSecret = "worker-callback-master-secret-not-server-signing-key"

func testScope() Scope {
	return Scope{
		TaskID: "task-1", RunID: "run-1", Operation: OperationDeliveryQA,
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String(),
	}
}

func TestMintedCapabilityBindsTaskRunOperationAndWorkerIdentity(t *testing.T) {
	now := time.Now().UTC()
	scope := testScope()
	token, err := Mint(testSigningKey, scope, 4*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(testSigningKey, token, scope.TaskID, scope.RunID, scope.Operation, now)
	if err != nil || verified != scope {
		t.Fatalf("valid capability scope=%#v err=%v, want %#v", verified, err, scope)
	}
	for _, mismatch := range []struct{ task, run, operation string }{
		{"task-2", scope.RunID, scope.Operation},
		{scope.TaskID, "run-2", scope.Operation},
		{scope.TaskID, scope.RunID, "delivery.plan"},
	} {
		if _, err := Verify(testSigningKey, token, mismatch.task, mismatch.run, mismatch.operation, now); err == nil {
			t.Fatalf("capability unexpectedly accepted mismatched request scope %#v", mismatch)
		}
	}
	// Identity is authenticated as part of the returned claims and can be
	// compared to the server-side task lease without trusting request fields.
	changed := scope
	changed.WorkerID = uuid.Must(uuid.NewV4()).String()
	if changed == verified {
		t.Fatal("different worker identity was not distinguishable")
	}
}

func TestCapabilityCannotBeMintedOrVerifiedWithWorkerCallbackMaster(t *testing.T) {
	scope := testScope()
	locallyForgedToken, err := Mint(testCallbackSecret, scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(testSigningKey, locallyForgedToken, scope.TaskID, scope.RunID, scope.Operation, time.Now().UTC()); err == nil {
		t.Fatal("server-only signing key accepted a capability signed with the worker callback master")
	}
	token, err := Mint(testSigningKey, scope, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(testCallbackSecret, token, scope.TaskID, scope.RunID, scope.Operation, time.Now().UTC()); err == nil {
		t.Fatal("worker callback master verified a server-issued capability")
	}
}

func TestCapabilityRejectsExpiredSignatureWrongKeyAndTampering(t *testing.T) {
	now := time.Now().UTC()
	scope := testScope()
	token, err := Mint(testSigningKey, scope, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(testSigningKey, token, scope.TaskID, scope.RunID, scope.Operation, now.Add(3*time.Minute)); err == nil {
		t.Fatal("expired capability was accepted")
	}
	if _, err := Verify(strings.Repeat("x", len(testSigningKey)), token, scope.TaskID, scope.RunID, scope.Operation, now); err == nil {
		t.Fatal("capability signed with another key was accepted")
	}
	parts := strings.Split(token, ".")
	parts[1] = strings.Repeat("A", len(parts[1]))
	if _, err := Verify(testSigningKey, strings.Join(parts, "."), scope.TaskID, scope.RunID, scope.Operation, now); err == nil {
		t.Fatal("tampered capability signature was accepted")
	}
}

func TestMintRejectsInvalidIdentityAndLongTTL(t *testing.T) {
	scope := testScope()
	for _, invalid := range []Scope{
		{TaskID: scope.TaskID, RunID: scope.RunID, Operation: scope.Operation},
		{TaskID: scope.TaskID, RunID: scope.RunID, Operation: scope.Operation, WorkerID: "worker-hostname", AgentKey: scope.AgentKey, MachineID: scope.MachineID},
		{TaskID: scope.TaskID, RunID: scope.RunID, Operation: scope.Operation, WorkerID: scope.WorkerID, AgentKey: "../generalist", MachineID: scope.MachineID},
	} {
		if _, err := Mint(testSigningKey, invalid, time.Minute); err == nil {
			t.Fatalf("Mint accepted invalid scope %#v", invalid)
		}
	}
	if _, err := Mint(testSigningKey, scope, MaxTTL+time.Second); err == nil {
		t.Fatal("Mint accepted a TTL longer than five minutes")
	}
}
