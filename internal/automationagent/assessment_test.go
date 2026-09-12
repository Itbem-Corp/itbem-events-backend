package automationagent

import "testing"

func TestParseReadOnlyAssessmentRejectsPatches(t *testing.T) {
	valid := `{"summary":"Vault and frozen source agree.","verdict":"assessed","evidence":["vault@abc"],"risks":[],"limitations":["No runtime probe was approved."],"recommended_next_steps":[]}`
	assessment, err := ParseReadOnlyAssessment(valid)
	if err != nil || assessment["verdict"] != "assessed" {
		t.Fatalf("expected bounded assessment: %#v / %v", assessment, err)
	}
	invalid := `{"summary":"no changes","verdict":"assessed","evidence":[],"risks":[],"limitations":[],"recommended_next_steps":[],"patch":"diff --git a/a b/a"}`
	if _, err := ParseReadOnlyAssessment(invalid); err == nil {
		t.Fatal("assessment containing a patch must fail closed")
	}
}

func TestParseReadOnlyAssessmentAllowsARecordedBlocker(t *testing.T) {
	blocked := `{"summary":"The source excerpt is incomplete.","verdict":"blocked","evidence":[],"risks":[],"limitations":["Missing frozen source."],"recommended_next_steps":["Refresh the Vault checkpoint."]}`
	assessment, err := ParseReadOnlyAssessment(blocked)
	if err != nil || assessment["verdict"] != "blocked" {
		t.Fatalf("expected the worker to retain an auditable blocker: %#v / %v", assessment, err)
	}
}

func TestParseReadOnlyAssessmentRejectsAnOversizedEvidenceList(t *testing.T) {
	evidence := `"evidence":["one","two","three","four","five","six","seven","eight","nine","ten","eleven","twelve","thirteen"]`
	oversized := `{"summary":"The result is bounded.","verdict":"assessed",` + evidence + `,"risks":[],"limitations":[],"recommended_next_steps":[]}`
	if _, err := ParseReadOnlyAssessment(oversized); err == nil {
		t.Fatal("oversized evidence must fail closed so the worker cannot persist an unbounded assessment")
	}
}
