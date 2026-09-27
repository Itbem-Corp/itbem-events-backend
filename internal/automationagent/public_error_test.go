package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func assertRedactedSecret(t *testing.T, value string) {
	t.Helper()
	for _, secret := range []string{"sk-live-api-secret", "bearer-secret-value", "url-password"} {
		if strings.Contains(value, secret) {
			t.Fatalf("public diagnostic leaked %q: %s", secret, value)
		}
	}
}

func publicErrorTestCredentialURL() string {
	return "https://" + "deploy" + ":" + "url-password" + "@example.test/repo"
}

func TestSafePublicErrorMessageRedactsCredentialsAndBoundsOutput(t *testing.T) {
	credentialURL := publicErrorTestCredentialURL()
	message := "validation failed: API_KEY=sk-live-api-secret; Authorization: Bearer bearer-secret-value; " + credentialURL
	safe := safePublicErrorMessage(message)
	assertRedactedSecret(t, safe)
	if !strings.Contains(safe, "validation failed") || !strings.Contains(safe, "<redacted>") {
		t.Fatalf("safe diagnostic lost useful context or redaction marker: %q", safe)
	}
	if got := safePublicErrorMessage(strings.Repeat("é", maxErrorMessageLen+10)); len(got) > maxErrorMessageLen {
		t.Fatalf("diagnostic exceeds byte limit: %d", len(got))
	} else if !json.Valid([]byte(`"` + got + `"`)) {
		t.Fatal("truncated diagnostic is not valid UTF-8")
	}
	if got := safePublicErrorMessage(""); got != "" {
		t.Fatalf("empty diagnostic should remain empty, got %q", got)
	}
}

func TestFinishImplementationAgentSanitizesPersistedAndCallbackFailure(t *testing.T) {
	w, store, _, callback, message, _, cp := agentFixture(t)
	cp.Calls = []agentCall{{Completion: Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: "{}"}, RequestRef: "request", ResponseRef: "response"}}
	credentialURL := publicErrorTestCredentialURL()
	cp.Failure = "acceptance criterion failed: API_KEY=sk-live-api-secret; Authorization: Bearer bearer-secret-value; " + credentialURL
	if err := w.finishImplementationAgent(context.Background(), message, cp.RunID, cp); err != nil {
		t.Fatal(err)
	}
	if len(callback.updates) != 1 {
		t.Fatalf("expected one callback update, got %d", len(callback.updates))
	}
	assertRedactedSecret(t, callback.updates[0].ErrorMessage)
	raw := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+cp.RunID+"/result.json"]
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	validationError, _ := output["validation_error"].(string)
	assertRedactedSecret(t, validationError)
}

func TestWorkerFailureBoundariesSanitizeCallbackAndPersistedDiagnostics(t *testing.T) {
	w, store, _, callback, message, _, cp := agentFixture(t)
	secretError := errors.New("tool failed: API_KEY=sk-live-api-secret; Authorization: Bearer bearer-secret-value; " + publicErrorTestCredentialURL())

	if err := w.fail(context.Background(), message.Payload.TaskID, cp.RunID, secretError); err != nil {
		t.Fatal(err)
	}
	assertRedactedSecret(t, callback.updates[len(callback.updates)-1].ErrorMessage)

	if err := w.failWithProviderResult(context.Background(), message.Payload.TaskID, cp.RunID, "request-ref", "delivery.implementation", Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3"}, secretError); err != nil {
		t.Fatal(err)
	}
	assertRedactedSecret(t, callback.updates[len(callback.updates)-1].ErrorMessage)
	raw := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+cp.RunID+"/result.json"]
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	validationError, _ := output["validation_error"].(string)
	assertRedactedSecret(t, validationError)
}

type recoveryCountingProvider struct {
	calls int
}

func (p *recoveryCountingProvider) Complete(context.Context, []Message, int) (Completion, error) {
	p.calls++
	return Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Content: `{"answer":"should not run"}`, Usage: map[string]any{"total_tokens": 1}}, nil
}

func TestRecoveryRejectsLegacyProviderResultWithoutVerifiableReceipt(t *testing.T) {
	message := validMessage()
	message.Payload.Operation = "ai.chat"
	legacy := map[string]any{
		"schema_version": 1, "task_id": message.Payload.TaskID,
		"provider": ProviderMiniMax, "model": "MiniMax-M3", "usage": map[string]any{"total_tokens": 1},
		"validation_error": "legacy result: API_KEY=sk-live-api-secret; Authorization: Bearer bearer-secret-value; " + publicErrorTestCredentialURL(),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(TaskInput{Prompt: "must not be sent to a provider"})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{
		input: input,
		existing: map[string][]byte{
			"itbem-ai-outputs-local/automation/" + message.Payload.TaskID + "/result.json": raw,
		},
	}
	callback := &fakeCallback{}
	provider := &recoveryCountingProvider{}
	w, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	err = w.Process(context.Background(), message)
	if err == nil || !strings.Contains(err.Error(), "private recovery accounting is invalid") {
		t.Fatalf("unverifiable legacy accounting must be rejected safely, got %v", err)
	}
	assertRedactedSecret(t, err.Error())
	if provider.calls != 0 {
		t.Fatalf("provider was invoked for a legacy result without receipt evidence: calls=%d", provider.calls)
	}
	if len(callback.updates) != 1 || callback.updates[0].Status != "running" {
		t.Fatalf("missing receipt must not produce success, failure details, or cost callback: %#v", callback.updates)
	}
	encodedCallback, err := json.Marshal(callback.updates)
	if err != nil {
		t.Fatal(err)
	}
	assertRedactedSecret(t, string(encodedCallback))
}

func TestPartialQAFailureSanitizesStoredAndCallbackError(t *testing.T) {
	w, store, _, callback, message, _, cp := agentFixture(t)
	secretError := errors.New("QA command failed: API_KEY=sk-live-api-secret; Authorization: Bearer bearer-secret-value; " + publicErrorTestCredentialURL())
	qaResult := map[string]any{"checks": []any{map[string]any{"name": "smoke", "output": "safe summary"}}}
	if err := w.failWithQAResult(context.Background(), message.Payload.TaskID, cp.RunID, qaResult, nil, secretError); err != nil {
		t.Fatal(err)
	}
	assertRedactedSecret(t, callback.updates[len(callback.updates)-1].ErrorMessage)
	raw := store.objects[w.config.OutputBucket+"/automation/"+message.Payload.TaskID+"/runs/"+cp.RunID+"/result.json"]
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	validationError, _ := output["validation_error"].(string)
	assertRedactedSecret(t, validationError)
	execution, _ := output["execution"].(map[string]any)
	qa, _ := execution["qa_execution"].(map[string]any)
	assertRedactedSecret(t, qa["error"].(string))
}
