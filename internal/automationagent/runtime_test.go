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
