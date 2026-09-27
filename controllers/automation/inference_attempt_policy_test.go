package automation

import (
	"strings"
	"testing"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestInferenceAttemptCallQuotaFollowsAdmissionBudgets(t *testing.T) {
	for operation, want := range map[string]int{
		"delivery.implementation": 6,
		"delivery.qa":             2,
		"delivery.plan":           1,
		"delivery.publish":        0,
		"automation.chat":         1,
	} {
		if got := inferenceAttemptCallQuota(operation); got != want {
			t.Errorf("quota(%q)=%d, want %d", operation, got, want)
		}
	}
}

func TestValidateAutomationInferenceAttemptPolicy(t *testing.T) {
	t.Setenv(attemptPolicySigningKeyEnv, strings.Repeat("c", 48))
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	taskID := uuid.Must(uuid.NewV4())
	projectID := uuid.Must(uuid.NewV4())
	routes := []models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3", ReasoningEnabled: true}}
	routesJSON, routesHash, err := canonicalInferenceRoutes(routes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: taskID, RunID: "attempt-1", Operation: "delivery.implementation",
		ProjectID: &projectID, PolicyRevision: 7, RoutesJSON: routesJSON, RoutesHash: routesHash,
		MaxCompletionTokens: 4096, MaxInferenceCalls: inferenceAttemptCallQuota("delivery.implementation"), CreatedAt: time.Now().UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		t.Fatal(err)
	}

	decoded, gotHash, valid := validateAutomationInferenceAttemptPolicy(snapshot)
	if !valid || gotHash != routesHash || len(decoded) != 1 || decoded[0] != routes[0] {
		t.Fatalf("valid frozen policy was rejected: valid=%v routes=%+v hash=%q", valid, decoded, gotHash)
	}
	// PostgreSQL jsonb may reorder fields and add spaces when it reads the
	// stored JSON. The signed semantic route must remain valid after that
	// database round trip.
	jsonbSnapshot := snapshot
	jsonbSnapshot.RoutesJSON = `[{"reasoning_enabled":true,"model":"MiniMax-M3","provider":"minimax"}]`
	decoded, gotHash, valid = validateAutomationInferenceAttemptPolicy(jsonbSnapshot)
	if !valid || gotHash != routesHash || len(decoded) != 1 || decoded[0] != routes[0] || !verifyAutomationInferenceAttemptPolicySignature(jsonbSnapshot) {
		t.Fatalf("jsonb-reformatted frozen policy was rejected: valid=%v routes=%+v hash=%q", valid, decoded, gotHash)
	}

	tests := []struct {
		name   string
		change func(*models.AutomationInferenceAttemptPolicy)
	}{
		{name: "route", change: func(value *models.AutomationInferenceAttemptPolicy) {
			value.RoutesJSON = `[{"provider":"deepseek","model":"DeepSeek-V4.1-Flash"}]`
		}},
		{name: "revision", change: func(value *models.AutomationInferenceAttemptPolicy) { value.PolicyRevision++ }},
		{name: "output limit", change: func(value *models.AutomationInferenceAttemptPolicy) { value.MaxCompletionTokens++ }},
		{name: "run quota", change: func(value *models.AutomationInferenceAttemptPolicy) { value.MaxInferenceCalls-- }},
		{name: "project", change: func(value *models.AutomationInferenceAttemptPolicy) {
			changed := uuid.Must(uuid.NewV4())
			value.ProjectID = &changed
		}},
		{name: "task", change: func(value *models.AutomationInferenceAttemptPolicy) { value.AutomationTaskID = uuid.Must(uuid.NewV4()) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			altered := snapshot
			test.change(&altered)
			if _, _, valid := validateAutomationInferenceAttemptPolicy(altered); valid {
				t.Fatal("altered attempt policy was accepted")
			}
		})
	}
}

func TestValidateAutomationInferenceAttemptPolicyAllowsFrozenNoRoute(t *testing.T) {
	t.Setenv(attemptPolicySigningKeyEnv, strings.Repeat("d", 48))
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: "attempt-no-route",
		Operation: "delivery.qa", RoutesJSON: "[]", RoutesHash: sha256Hex([]byte("[]")),
		MaxInferenceCalls: inferenceAttemptCallQuota("delivery.qa"), CreatedAt: time.Now().UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		t.Fatal(err)
	}

	routes, _, valid := validateAutomationInferenceAttemptPolicy(snapshot)
	if !valid || len(routes) != 0 {
		t.Fatalf("empty policy snapshot should validate but remain route-less: valid=%v routes=%v", valid, routes)
	}
}

func TestAutomationInferenceAttemptPolicySignatureDetectsRecomputedHashTamperingAndRotates(t *testing.T) {
	oldKey, newKey := strings.Repeat("o", 48), strings.Repeat("n", 48)
	t.Setenv(attemptPolicySigningKeyEnv, oldKey)
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	routesJSON, routesHash, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{{Provider: "minimax", Model: "MiniMax-M3"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := models.AutomationInferenceAttemptPolicy{
		ID: uuid.Must(uuid.NewV4()), AutomationTaskID: uuid.Must(uuid.NewV4()), RunID: "signed-attempt",
		Operation: "delivery.plan", RoutesJSON: routesJSON, RoutesHash: routesHash,
		MaxCompletionTokens: 2048, MaxInferenceCalls: inferenceAttemptCallQuota("delivery.plan"), CreatedAt: time.Now().UTC(),
	}
	snapshot.SnapshotHash = inferenceAttemptSnapshotHash(snapshot)
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !verifyAutomationInferenceAttemptPolicySignature(snapshot) {
		t.Fatal("a correctly signed attempt recipe did not verify")
	}

	t.Setenv(attemptPolicySigningKeyEnv, newKey)
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, oldKey)
	if !verifyAutomationInferenceAttemptPolicySignature(snapshot) {
		t.Fatal("the previous key did not verify an in-flight attempt during rotation")
	}

	tampered := snapshot
	tamperedRoutesJSON, tamperedRoutesHash, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{{Provider: "deepseek", Model: "deepseek-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	tampered.RoutesJSON, tampered.RoutesHash = tamperedRoutesJSON, tamperedRoutesHash
	tampered.SnapshotHash = inferenceAttemptSnapshotHash(tampered)
	if _, _, structurallyValid := validateAutomationInferenceAttemptPolicy(tampered); !structurallyValid {
		t.Fatal("fixture must model a database writer who can recompute unkeyed hashes")
	}
	if verifyAutomationInferenceAttemptPolicySignature(tampered) {
		t.Fatal("recomputed unkeyed hashes must not forge the server HMAC")
	}

	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	if verifyAutomationInferenceAttemptPolicySignature(snapshot) {
		t.Fatal("a previous-key signature verified after its rotation overlap was removed")
	}
	newSnapshot := snapshot
	newSnapshot.RunID = "new-key-attempt"
	newSnapshot.SnapshotHash = inferenceAttemptSnapshotHash(newSnapshot)
	if err := signAutomationInferenceAttemptPolicy(&newSnapshot); err != nil || !verifyAutomationInferenceAttemptPolicySignature(newSnapshot) {
		t.Fatalf("new recipes should sign with the active rotation key: err=%v", err)
	}
}

func TestAutomationInferenceAttemptPolicySigningFailsClosedWithoutServerKey(t *testing.T) {
	t.Setenv(attemptPolicySigningKeyEnv, "")
	t.Setenv(attemptPolicyPreviousSigningKeyEnv, "")
	snapshot := models.AutomationInferenceAttemptPolicy{SnapshotHash: strings.Repeat("a", 64)}
	if err := signAutomationInferenceAttemptPolicy(&snapshot); err == nil {
		t.Fatal("attempt recipes must not fall back to the callback or provider key when the server key is absent")
	}
	if verifyAutomationInferenceAttemptPolicySignature(snapshot) {
		t.Fatal("unsigned attempts must be rejected when no server key is configured")
	}
}
