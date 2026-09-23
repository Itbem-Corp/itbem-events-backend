package automationagent

import "testing"

func TestRuntimeConfigRequiresProviderCapabilities(t *testing.T) {
	values := map[string]string{
		"ITBEM_AI_QUEUE_URL":         "http://127.0.0.1:4566/000000000000/itbem-ai-local",
		"AWS_REGION":                 "us-east-1",
		"ITBEM_API_BASE_URL":         "http://127.0.0.1:18080",
		"AUTOMATION_CALLBACK_SECRET": "local-secret",
		"ITBEM_AI_INPUT_BUCKET":      "itbem-ai-inputs-local",
		"ITBEM_AI_OUTPUT_BUCKET":     "itbem-ai-outputs-local",
		"ITBEM_AI_SQS_ENDPOINT":      "http://127.0.0.1:4566",
		"ITBEM_AI_S3_ENDPOINT":       "http://127.0.0.1:4566",
	}
	config, err := LoadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if !config.RequireProviderCapabilities {
		t.Fatal("production runtime workers must require a versioned provider capability contract")
	}
}
