package automationagent

import (
	"os"
	"strings"
	"testing"
)

// The old opt-in harness read a project API key and constructed a vendor
// adapter in-process. Keep a source-level regression guard on the operator
// entrypoint so future changes cannot quietly reintroduce that bypass.
func TestAgentHarnessRunnerHasNoProviderCredentialOrDirectAdapterPath(t *testing.T) {
	runner, err := os.ReadFile("../../scripts/Test-AgentHarness.ps1")
	if err != nil {
		t.Fatal("could not read the harness runner source")
	}
	source := string(runner)
	for _, forbidden := range []string{
		".env.ai.local",
		"NewProviderClient(",
		"LoadProviderConfig(",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("operator harness regained a forbidden provider path (%s)", forbidden)
		}
	}
	if !strings.Contains(source, "Paid live-provider harness mode is retired") {
		t.Fatal("legacy paid live-mode selectors must fail closed with an actionable message")
	}
	if !strings.Contains(source, "foreach ($name in $providerEnvironmentNames) { [Environment]::SetEnvironmentVariable($name,'','Process') }") {
		t.Fatal("offline Go tests must receive an explicitly scrubbed provider environment")
	}
	for _, isolatedName := range []string{
		"MINIMAX_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_GO_API_KEY",
		"ITBEM_AI_PROVIDER", "ITBEM_AI_GATEWAY_URL",
	} {
		if !strings.Contains(source, "'"+isolatedName+"'") {
			t.Fatalf("provider environment variable %q is not isolated", isolatedName)
		}
	}
}

func TestAgentHarnessRunnerKeepsOfflineAndReportReplayModes(t *testing.T) {
	runner, err := os.ReadFile("../../scripts/Test-AgentHarness.ps1")
	if err != nil {
		t.Fatal("could not read the harness runner source")
	}
	source := string(runner)
	for _, required := range []string{
		"-ScoreReportPath",
		"$goCommand test @testPackages",
		"'./internal/automationagent','./controllers/delivery','./controllers/automation'",
		"Offline harness summary:",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("safe harness mode %q is missing", required)
		}
	}
}
