package automationagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPersistedFinalAnswerPreservesTypedReasoningSettingsWithoutPrivateThoughts(t *testing.T) {
	for _, effort := range []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		body, _ := json.Marshal(map[string]any{"reasoning_enabled": true, "reasoning_effort": effort, "reasoning_content": "PRIVATE_CANARY", "nested": map[string]any{"analysis": "PRIVATE_CANARY", "reasoning_enabled": false}})
		completion, err := sanitizeProviderCompletionForPersistence(Completion{Content: string(body)})
		if err != nil || strings.Contains(completion.Content, "PRIVATE_CANARY") {
			t.Fatalf("private content crossed persistence boundary: %v", err)
		}
		var answer map[string]any
		if err := json.Unmarshal([]byte(completion.Content), &answer); err != nil {
			t.Fatal(err)
		}
		if answer["reasoning_enabled"] != true || answer["reasoning_effort"] != effort || mapValue(answer["nested"])["reasoning_enabled"] != false {
			t.Fatalf("public settings lost for effort %q", effort)
		}
	}
}

func TestReasoningSettingNamesCannotSmugglePrivateValues(t *testing.T) {
	for _, sample := range []struct {
		key   string
		value any
	}{
		{"reasoning_enabled", "PRIVATE_CANARY"},
		{"reasoning_enabled", 1},
		{"reasoning_enabled", map[string]any{"content": "PRIVATE_CANARY"}},
		{"reasoning_effort", "PRIVATE_CANARY"},
		{"reasoning_effort", []any{"PRIVATE_CANARY"}},
		{"reasoning_effort", map[string]any{"content": "PRIVATE_CANARY"}},
		{"Reasoning_Enabled", true},
		{"reasoning_enabled_content", "PRIVATE_CANARY"},
	} {
		got := sanitizeAgentStoredValue(map[string]any{sample.key: sample.value, "summary": "public"}, false).(map[string]any)
		if _, retained := got[sample.key]; retained || got["summary"] != "public" {
			t.Fatalf("invalid setting retained under %q", sample.key)
		}
	}
}
