package automation

import (
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/internal/automationagent"
	"events-stocks/models"
)

func TestInferenceGatewayRequestEnforcesProviderAndOutputBounds(t *testing.T) {
	valid := inferenceRequest{
		CallID: "6a07c5a1-4025-4e2e-bf0e-0560bcfb3ec7", Provider: "minimax", Model: "MiniMax-M3",
		Messages: []automationagent.Message{{Role: "user", Content: "hello"}}, MaxCompletionTokens: 8192,
	}
	if !validInferenceGatewayRequest(valid, automationagent.ProviderMiniMax) {
		t.Fatal("request at the gateway output ceiling should remain valid")
	}
	invalid := valid
	invalid.Provider = "unknown-provider"
	if validInferenceGatewayRequest(invalid, automationagent.Provider(invalid.Provider)) {
		t.Fatal("unknown provider was accepted by the gateway contract")
	}
	invalid = valid
	invalid.MaxCompletionTokens = 8193
	if validInferenceGatewayRequest(invalid, automationagent.ProviderMiniMax) {
		t.Fatal("request above the hard output-token ceiling was accepted")
	}
	invalid = valid
	invalid.CallID = ""
	if validInferenceGatewayRequest(invalid, automationagent.ProviderMiniMax) {
		t.Fatal("request without an idempotent call identity was accepted")
	}
	stepBound := valid
	stepBound.Operation = "delivery.implementation"
	stepBound.PlanStepID = "29b04756-e851-47c0-a989-1e3b84eb8c39"
	if !validInferenceGatewayRequest(stepBound, automationagent.ProviderMiniMax) {
		t.Fatal("canonical implementation plan_step_id should be accepted")
	}
	for _, malformed := range []string{" ", " not-a-uuid", strings.ToUpper(stepBound.PlanStepID)} {
		stepBound.PlanStepID = malformed
		if validInferenceGatewayRequest(stepBound, automationagent.ProviderMiniMax) {
			t.Fatalf("non-canonical plan_step_id %q was accepted", malformed)
		}
	}
	stepBound.PlanStepID = "29b04756-e851-47c0-a989-1e3b84eb8c39"
	stepBound.Operation = "delivery.plan"
	if validInferenceGatewayRequest(stepBound, automationagent.ProviderMiniMax) {
		t.Fatal("task-level planning request must not claim a delivery plan step")
	}
}

func TestInferenceRequestRejectsCredentialFieldsWithoutEchoingTheirValue(t *testing.T) {
	const secret = "server-provider-key-canary"
	decoder := json.NewDecoder(strings.NewReader(`{"provider":"minimax","model":"MiniMax-M3","api_key":"` + secret + `"}`))
	decoder.DisallowUnknownFields()
	var request inferenceRequest
	err := decoder.Decode(&request)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("worker inference contract accepted or disclosed a credential field: %v", err)
	}
}

func TestInferenceGatewayMessagesAreBoundedBeforeProviderCalls(t *testing.T) {
	if !validInferenceMessages([]automationagent.Message{{Role: "user", Content: strings.Repeat("x", maxInferenceGatewayMessageBytes)}}) {
		t.Fatal("message exactly at per-message byte ceiling was rejected")
	}
	for _, messages := range [][]automationagent.Message{
		{{Role: "user", Content: strings.Repeat("x", maxInferenceGatewayMessageBytes+1)}},
		{{Role: "tool", Content: "not an accepted role"}},
		{{Role: "user", Content: strings.Repeat("x", maxInferenceGatewayMessageBytes)}, {Role: "assistant", Content: strings.Repeat("y", maxInferenceGatewayRequestBytes-maxInferenceGatewayMessageBytes+1)}},
	} {
		if validInferenceMessages(messages) {
			t.Fatalf("oversized or invalid messages were accepted (messages=%d)", len(messages))
		}
	}
}

func TestCanonicalInferenceRoutesRejectInconsistentReasoningAndDuplicates(t *testing.T) {
	tests := []models.AutomationAIActionRoute{
		{Provider: "minimax", Model: "MiniMax-M3", ReasoningEnabled: false, ReasoningEffort: "medium"},
		{Provider: "minimax", Model: "MiniMax-M3", ReasoningEnabled: true, ReasoningEffort: "ultra-high"},
	}
	for _, route := range tests {
		if _, _, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{route}); err == nil {
			t.Fatalf("inconsistent reasoning policy was accepted: %#v", route)
		}
	}
	duplicate := models.AutomationAIActionRoute{Provider: "minimax", Model: "MiniMax-M3"}
	if _, _, err := canonicalInferenceRoutes([]models.AutomationAIActionRoute{duplicate, duplicate}); err == nil {
		t.Fatal("duplicate routes were accepted")
	}
}
