package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutomationExecutionTraceNeverSerializesPrivateObjectReferences(t *testing.T) {
	encoded, err := json.Marshal(AutomationExecution{
		RequestRef:  "s3://itbem-ai-inputs-local/automation/inputs/private/input.json",
		ResponseRef: "s3://itbem-ai-outputs-local/automation/private/result.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"request_ref", "response_ref", "itbem-ai-inputs-local", "itbem-ai-outputs-local"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("automation execution trace leaked %q: %s", forbidden, payload)
		}
	}
}

func TestAutomationToolExecutionTraceNeverSerializesPrivateObjectReferences(t *testing.T) {
	encoded, err := json.Marshal(AutomationToolExecution{
		RequestRef:  "s3://itbem-ai-outputs-local/automation/task/artifacts/semantic-qa.json",
		ResponseRef: "s3://itbem-ai-outputs-local/automation/task/artifacts/semantic-qa.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"request_ref", "response_ref", "itbem-ai-outputs-local"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("automation tool execution trace leaked %q: %s", forbidden, payload)
		}
	}
}
