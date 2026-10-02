package automationagent

import (
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/internal/qaevidence"
)

func TestQALedgerObservationPreservesObservedFailuresAndRejectsMalformedEvidence(t *testing.T) {
	const task = "a4a4b837-2e18-43af-9f58-6d59629db2bb"
	result := map[string]any{
		"preview":                    map[string]any{"passed": false},
		"repository_execution_order": []string{"workspace://repo"},
		"repository_runs": []any{map[string]any{
			"workspace": "workspace://repo", "branch": "itbem-agent/" + task,
			"commands": []any{
				map[string]any{"phase": "validation", "kind": "unit", "passed": true},
				map[string]any{"phase": "qa", "kind": "security", "passed": false},
			},
		}},
		"verdict": "passed", "summary": "untrusted model claims success",
	}
	handoff, err := qaLedgerObservation(task, strings.Repeat("a", 64), result)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := qaevidence.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if observed.PreviewPassed || observed.TaskID != task || len(observed.Repositories) != 1 || len(observed.Repositories[0].Commands) != 2 || !observed.Repositories[0].Commands[0].Passed || observed.Repositories[0].Commands[1].Passed || observed.Repositories[0].Commands[1].Index != 1 {
		t.Fatalf("execution was replaced by a summary: %#v", observed)
	}
	if strings.Contains(string(raw), "verdict") || strings.Contains(string(raw), "summary") {
		t.Fatal("model assertions entered ledger")
	}
	result["repository_execution_order"] = []string{"workspace://other"}
	if _, err := qaLedgerObservation(task, strings.Repeat("a", 64), result); err == nil {
		t.Fatal("incomplete execution order accepted")
	}
	if value, err := qaLedgerObservation(task, "", result); err != nil || value != nil {
		t.Fatal("historical diagnostic invented a ledger subject")
	}
}
