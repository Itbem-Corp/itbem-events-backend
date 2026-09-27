package automationagent

// InferenceOperations is the single allowlist for work that can make a model
// call. It is deliberately separate from deterministic automation actions
// such as delivery.publish. The cloud gateway requires a persisted route for
// every operation in this list before it can obtain a provider credential.
var InferenceOperations = []string{
	"ai.chat",
	"document.analyze",
	"code.review",
	"product.ideate",
	"delivery.chat",
	"delivery.plan",
	"delivery.implementation",
	"delivery.assessment",
	"delivery.qa",
	"delivery.summary",
}

func IsInferenceOperation(operation string) bool {
	for _, candidate := range InferenceOperations {
		if operation == candidate {
			return true
		}
	}
	return false
}
