package automationagent

import (
	"events-stocks/services/automationcost"
	"testing"
)

func TestReviewUsageKeepsNestedCounts(t *testing.T) {
	call := Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Usage: map[string]any{
		"prompt_tokens": 100, "completion_tokens": 40,
		"prompt_tokens_details":     map[string]any{"cached_tokens": 80},
		"completion_tokens_details": map[string]any{"reasoning_tokens": 30},
	}}
	call.CallID, call.ReceiptID = stepCallbackUUID(), stepCallbackUUID()
	result, err := aggregateCodeReviewCompletions([]Completion{call, call}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := automationcost.Build(string(result.Provider), result.Model, result.Usage, "")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.CachedInputTokens != 160 || ledger.ReasoningTokens != 60 || ledger.TotalTokens != 280 {
		t.Fatalf("segment counts lost: %#v", ledger)
	}
	if result.CallID != call.CallID || result.ReceiptID != call.ReceiptID {
		t.Fatal("receipt binding lost")
	}
}

func TestPlanRepairKeepsReceiptAndUsage(t *testing.T) {
	usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 40, "prompt_tokens_details": map[string]any{"cached_tokens": 80}, "completion_tokens_details": map[string]any{"reasoning_tokens": 30}}
	candidate := Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Usage: usage}
	repair := Completion{Provider: ProviderMiniMax, Model: "MiniMax-M3", Usage: usage, CallID: stepCallbackUUID(), ReceiptID: stepCallbackUUID(), Content: "final", ResponseID: "last"}
	callID, receiptID := repair.CallID, repair.ReceiptID
	if err := attachDeliveryPlanRepairAccounting(&repair, candidate, "private-ref"); err != nil {
		t.Fatal(err)
	}
	ledger, err := automationcost.Build(string(repair.Provider), repair.Model, repair.Usage, "")
	if err != nil {
		t.Fatal(err)
	}
	if repair.CallID != callID || repair.ReceiptID != receiptID || repair.Content != "final" || repair.ResponseID != "last" {
		t.Fatal("repair destroyed final receipt identity or response")
	}
	if ledger.CachedInputTokens != 160 || ledger.ReasoningTokens != 60 {
		t.Fatal("repair lost nested usage")
	}
}

func TestReasoningOffIsExplicit(t *testing.T) {
	for _, provider := range []Provider{ProviderDeepSeek, ProviderOpenRouter} {
		config := ProviderConfig{Provider: provider, Model: "future-model", ReasoningEnabled: false, ReasoningEffort: "high"}
		payload, _ := newProviderTestClient(config, nil).payload([]Message{{Role: "user", Content: "test"}}, 100)
		if provider == ProviderDeepSeek {
			if payload["thinking"].(map[string]string)["type"] != "disabled" || payload["reasoning_effort"] != nil {
				t.Fatal("disabled policy deferred to provider thinking default")
			}
		} else {
			reasoning := payload["reasoning"].(map[string]any)
			if reasoning["enabled"] != false || reasoning["effort"] != nil {
				t.Fatal("disabled route retained reasoning effort")
			}
		}
		config.ReasoningEnabled = true
		payload, _ = newProviderTestClient(config, nil).payload([]Message{{Role: "user", Content: "test"}}, 100)
		if provider == ProviderDeepSeek {
			if payload["thinking"].(map[string]string)["type"] != "enabled" || payload["reasoning_effort"] != "high" {
				t.Fatal("enabled policy changed")
			}
		} else if payload["reasoning"].(map[string]any)["effort"] != "high" {
			t.Fatal("enabled effort changed")
		}
	}
}

func TestReasoningWireAudit(t *testing.T) {
	for _, tc := range []struct {
		provider                 Provider
		model                    string
		enabled                  bool
		effort, mode, wireEffort string
	}{
		{ProviderDeepSeek, "deepseek-flash", false, "high", "disabled", ""},
		{ProviderDeepSeek, "deepseek-flash", true, "low", "enabled", "low"},
		{ProviderOpenRouter, "future/model", false, "high", "disabled", ""},
		{ProviderOpenRouter, "future/model", true, "high", "enabled", "high"},
		{ProviderMiniMax, "MiniMax-M3", false, "", "disabled", ""},
		{ProviderMiniMax, "MiniMax-M3", true, "", "omitted", ""},
		{ProviderOpenAI, "gpt-6-sol", true, "high", "effort", "high"},
		{ProviderAnthropic, "claude-test", true, "high", "omitted", ""},
	} {
		client := newProviderTestClient(ProviderConfig{Provider: tc.provider, Model: tc.model, ReasoningEnabled: tc.enabled, ReasoningEffort: tc.effort, secret: "private"}, nil)
		mode, effort := ReasoningWireSettings(client)
		if mode != tc.mode || effort != tc.wireEffort {
			t.Fatalf("wire control %s/%s: %s/%s", tc.provider, tc.model, mode, effort)
		}
	}
}
