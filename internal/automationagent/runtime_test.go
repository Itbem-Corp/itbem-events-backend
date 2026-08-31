package automationagent

import "testing"

func runtimeTestEnvironment(overrides map[string]string) func(string) string {
	values := map[string]string{
		"ITBEM_AI_QUEUE_URL":         "https://sqs.example/queue",
		"AWS_REGION":                 "us-east-2",
		"ITBEM_API_BASE_URL":         "https://api.example.com",
		"AUTOMATION_CALLBACK_SECRET": "callback-secret",
		"ITBEM_AI_INPUT_BUCKET":      "itbem-ai-inputs-test",
		"ITBEM_AI_OUTPUT_BUCKET":     "itbem-ai-outputs-test",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string { return values[key] }
}

func TestLoadRuntimeConfigAcceptsLegacyOrExactRoleIdentity(t *testing.T) {
	legacy, err := LoadRuntimeConfig(runtimeTestEnvironment(nil))
	if err != nil || legacy.Role != "" || legacy.Lane != "" {
		t.Fatalf("legacy runtime = %#v, %v", legacy, err)
	}
	reviewer, err := LoadRuntimeConfig(runtimeTestEnvironment(map[string]string{"ITBEM_AI_ROLE": "reviewer", "ITBEM_AI_QUEUE_LANE": "review"}))
	if err != nil || reviewer.Role != "reviewer" || reviewer.Lane != "review" {
		t.Fatalf("review runtime = %#v, %v", reviewer, err)
	}
}

func TestLoadRuntimeConfigRejectsPartialOrCrossRoleIdentity(t *testing.T) {
	for name, overrides := range map[string]map[string]string{
		"role only":  {"ITBEM_AI_ROLE": "reviewer"},
		"lane only":  {"ITBEM_AI_QUEUE_LANE": "review"},
		"cross role": {"ITBEM_AI_ROLE": "reviewer", "ITBEM_AI_QUEUE_LANE": "release"},
		"invented":   {"ITBEM_AI_ROLE": "admin", "ITBEM_AI_QUEUE_LANE": "production"},
	} {
		t.Run(name, func(t *testing.T) {
			if config, err := LoadRuntimeConfig(runtimeTestEnvironment(overrides)); err == nil {
				t.Fatalf("invalid runtime identity accepted: %#v", config)
			}
		})
	}
}

func TestLoadRuntimeConfigValidatesQueueTransport(t *testing.T) {
	for name, queueURL := range map[string]string{
		"placeholder":        "QUEUE_URL_FROM_STACK_OUTPUT",
		"insecure remote":    "http://sqs.example/queue",
		"missing queue path": "https://sqs.example/",
		"embedded identity":  "https://operator@sqs.example/queue",
		"query mutation":     "https://sqs.example/queue?override=true",
	} {
		t.Run(name, func(t *testing.T) {
			if config, err := LoadRuntimeConfig(runtimeTestEnvironment(map[string]string{"ITBEM_AI_QUEUE_URL": queueURL})); err == nil {
				t.Fatalf("unsafe queue transport accepted: %#v", config)
			}
		})
	}
	if _, err := LoadRuntimeConfig(runtimeTestEnvironment(map[string]string{"ITBEM_AI_QUEUE_URL": "http://127.0.0.1:4566/000000000000/local-queue"})); err != nil {
		t.Fatalf("loopback development queue rejected: %v", err)
	}
}

func TestLoadRuntimeConfigAcceptsProductAgnosticBuckets(t *testing.T) {
	config, err := LoadRuntimeConfig(runtimeTestEnvironment(map[string]string{
		"ITBEM_AI_INPUT_BUCKET":  "acme-control-inputs-prod",
		"ITBEM_AI_OUTPUT_BUCKET": "acme-control-evidence-prod",
	}))
	if err != nil || config.InputBucket != "acme-control-inputs-prod" || config.OutputBucket != "acme-control-evidence-prod" {
		t.Fatalf("generic runtime storage = %#v, %v", config, err)
	}
}
