package automationagent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryDecisionClaimsBindEachGate(t *testing.T) {
	context := json.RawMessage(`{"gates":[{"id":"gate-plan","kind":"plan","decision":"approved"},{"id":"gate-qa","kind":"qa_review","decision":"request_changes"}]}`)
	baseline := `{"technical":{"decision_claims":{"schema_version":1,"gates":[{"index":0,"gate_id":"gate-plan","kind":"plan","decision":"approved"},{"index":1,"gate_id":"gate-qa","kind":"qa_review","decision":"request_changes"}]}}}`
	if claims, err := ValidateSummaryDecisionClaims(baseline, context); err != nil || len(claims) != 2 {
		t.Fatalf("valid claims rejected: %v", err)
	}
	for _, report := range []string{
		`{"technical":{"decisions":["Plan not approved; QA approved"]}}`,
		`{"technical":{"decision_claims":null}}`,
		strings.Replace(baseline, `"index":0`, `"index":1`, 1),
		strings.Replace(baseline, `"index":0`, `"index":null`, 1),
		strings.Replace(baseline, `"index":0`, `"index":0,"INDEX":1`, 1),
		strings.Replace(baseline, `"gate_id":"gate-plan"`, `"gate_id":"invented"`, 1),
		strings.Replace(baseline, `"kind":"plan"`, `"kind":"qa_review"`, 1),
		strings.ReplaceAll(strings.ReplaceAll(baseline, "approved", "swap"), "request_changes", "approved"),
		strings.Replace(baseline, `"schema_version":1`, `"schema_version":2`, 1),
		baseline + ` {}`,
	} {
		if _, err := ValidateSummaryDecisionClaims(report, context); err == nil {
			t.Fatalf("ungrounded claims accepted: %s", report)
		}
	}
	if _, err := ValidateSummaryDecisionClaims(`{"technical":{"decisions":[]}}`, json.RawMessage(`{"gates":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateSummaryDecisionClaims(baseline, json.RawMessage(`{"gates":[]}`)); err == nil {
		t.Fatal("invented gates accepted")
	}
}

func TestSummaryDecisionRenderingPreservesRecordedGatePairs(t *testing.T) {
	context := json.RawMessage(`{"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test"}],"gates":[{"id":"gate-plan","kind":"plan","decision":"approved"},{"id":"gate-qa","kind":"qa_review","decision":"request_changes"}]}`)
	base := `{"executive":{"what_changed":"Synthetic flow","why":"Ground decisions","how_to_test":"Read evidence","risks":["Human gate"]},"technical":{"decisions":["Plan not approved; QA approved"],"evidence":["1e5c5eb5-38cb-46af-9e50-a196ad7fc333 — Synthetic authorization test"]`
	claims := `,"decision_claims":{"schema_version":1,"gates":[{"index":0,"gate_id":"gate-plan","kind":"plan","decision":"approved"},{"index":1,"gate_id":"gate-qa","kind":"qa_review","decision":"request_changes"}]}`
	for _, report := range []string{base + `}}`, base + strings.ReplaceAll(strings.ReplaceAll(claims, "approved", "swap"), "request_changes", "approved") + `}}`} {
		summary, err := ParseDeliverySummary(report)
		if err != nil {
			t.Fatal(err)
		}
		if ValidateDeliverySummaryEvidence(summary, context) == nil {
			t.Fatal("absent or swapped claims admitted")
		}
	}
	summary, err := ParseDeliverySummary(base + claims + `}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeliverySummaryEvidence(summary, context); err != nil {
		t.Fatal(err)
	}
	technical := summary["technical"].(map[string]any)
	decisions := technical["decisions"].([]any)
	if len(decisions) != 2 || !strings.Contains(decisions[0].(string), "plan: approved") || !strings.Contains(decisions[1].(string), "qa_review: request_changes") {
		t.Fatalf("gate results were not paired: %v", decisions)
	}
	if strings.Contains(decisions[0].(string), "not approved") || technical["_harness_repairs"] == nil {
		t.Fatal("model prose was promoted or rendering was hidden")
	}
}
