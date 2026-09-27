package automationagent

import "testing"

func TestInferenceOperationRegistryIncludesEveryModelActionAndExcludesDeterministicPublishing(t *testing.T) {
	for _, operation := range []string{
		"ai.chat", "document.analyze", "code.review", "product.ideate",
		"delivery.chat", "delivery.plan", "delivery.implementation", "delivery.assessment", "delivery.qa", "delivery.summary",
	} {
		if !IsInferenceOperation(operation) {
			t.Fatalf("%s must require a cloud inference route", operation)
		}
	}
	if IsInferenceOperation("delivery.publish") {
		t.Fatal("deterministic publication must not receive an AI route")
	}
}
