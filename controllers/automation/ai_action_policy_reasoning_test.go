package automation

import (
	"events-stocks/internal/automationagent"
	"testing"
)

func TestReasoningSelectionRequiresAnAdapterSupportedControl(t *testing.T) {
	for _, sample := range []struct {
		name     string
		provider automationagent.Provider
		model    automationagent.ProviderModel
		effort   string
		want     bool
	}{
		{"M3 binary enabled", automationagent.ProviderMiniMax, automationagent.ProviderModel{ID: "MiniMax-M3", SupportsReasoning: true}, "", true},
		{"M3 does not invent high", automationagent.ProviderMiniMax, automationagent.ProviderModel{ID: "MiniMax-M3", SupportsReasoning: true}, "high", false},
		{"M3 capability required", automationagent.ProviderMiniMax, automationagent.ProviderModel{ID: "MiniMax-M3"}, "", false},
		{"M2 not configurable", automationagent.ProviderMiniMax, automationagent.ProviderModel{ID: "MiniMax-M2.7", SupportsReasoning: true}, "", false},
		{"other adapter no binary exception", automationagent.ProviderOpenRouter, automationagent.ProviderModel{ID: "MiniMax-M3", SupportsReasoning: true}, "", false},
		{"DeepSeek high", automationagent.ProviderDeepSeek, automationagent.ProviderModel{ID: "deepseek-flash", SupportsReasoning: true, ReasoningEfforts: []string{"low", "high", "max"}}, "high", true},
		{"DeepSeek needs a level", automationagent.ProviderDeepSeek, automationagent.ProviderModel{ID: "deepseek-flash", SupportsReasoning: true, ReasoningEfforts: []string{"low", "high", "max"}}, "", false},
		{"unsupported model rejects levels", automationagent.ProviderDeepSeek, automationagent.ProviderModel{ID: "plain", ReasoningEfforts: []string{"high"}}, "high", false},
		{"Luna medium", automationagent.ProviderOpenRouter, automationagent.ProviderModel{ID: "openai/gpt-6-luna", SupportsReasoning: true, ReasoningEfforts: []string{"medium", "high"}}, "medium", true},
		{"Luna missing level rejected", automationagent.ProviderOpenRouter, automationagent.ProviderModel{ID: "openai/gpt-6-luna", SupportsReasoning: true, ReasoningEfforts: []string{"medium", "high"}}, "max", false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if got := modelSupportsReasoningSelection(sample.provider, sample.model, sample.effort); got != sample.want {
				t.Fatalf("reasoning selection allowed=%t, want %t", got, sample.want)
			}
		})
	}
}
