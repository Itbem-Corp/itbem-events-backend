package automationagent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestQAReportGroundingRequiresObservedModelClaims(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile("../qaevidence/testdata/grounding/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	var observation map[string]any
	if err := json.Unmarshal([]byte(read("observation")), &observation); err != nil {
		t.Fatal(err)
	}
	base := `{"summary":"Observed failure","verdict":"failed","checks":[{"name":"Security","status":"failed","detail":"Recorded failure"}],"defects":[],"coverage_gaps":[],"recommended_actions":[]`
	for _, tc := range []struct {
		name, content string
		execution     map[string]any
		promoted      bool
		status        string
	}{
		{"correct", base + `,"claims":` + read("grounded-claims") + `}`, map[string]any{"ledger_observation": observation}, true, "passed"},
		{"invented", base + `,"claims":` + read("invented-claims") + `}`, map[string]any{"ledger_observation": observation}, false, "failed"},
		{"missing", base + `}`, map[string]any{"ledger_observation": observation}, false, "failed"},
		{"null claims", base + `,"claims":null}`, map[string]any{"ledger_observation": observation}, false, "failed"},
		{"null ledger", base + `}`, map[string]any{"ledger_observation": nil}, false, "failed"},
		{"historical", base + `}`, map[string]any{}, true, "unavailable"},
		{"ambiguous", base + `,"claims":` + read("grounded-claims") + `,"CLAIMS":` + read("grounded-claims") + `}`, map[string]any{"ledger_observation": observation}, false, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, score := qaReportWithGrounding(tc.content, tc.execution)
			if (report != nil) != tc.promoted || score["status"] != tc.status {
				t.Fatalf("report=%v score=%v", report, score)
			}
			if report != nil && tc.status == "passed" && report["verdict"] != "failed" {
				t.Fatal("grounding success changed failed QA verdict")
			}
		})
	}
	// All claims and narrative checks pass here. Each negative case must fail
	// solely because the separate runtime semantic/screenshot evidence failed.
	for _, repository := range observation["repositories"].([]any) {
		for _, command := range repository.(map[string]any)["commands"].([]any) {
			command.(map[string]any)["passed"] = true
		}
	}
	passingClaims := strings.ReplaceAll(read("grounded-claims"), `"passed": false`, `"passed": true`)
	passingClaims = strings.Replace(passingClaims, `"verdict": "failed"`, `"verdict": "passed"`, 1)
	passingReport := strings.ReplaceAll(base, `"failed"`, `"passed"`) + `,"claims":` + passingClaims + `}`
	for _, failure := range []string{"none", "semantic", "screenshot"} {
		t.Run("runtime guard/"+failure, func(t *testing.T) {
			execution := map[string]any{
				"ledger_observation": observation,
				"preview":            map[string]any{"passed": true},
				"repository_runs":    []any{map[string]any{"commands": []any{map[string]any{"passed": true}}}},
			}
			if failure != "none" {
				execution[failure] = map[string]any{"passed": false}
			}
			report, score := qaReportWithGrounding(passingReport, execution)
			if failure == "none" {
				if report == nil || score["status"] != "passed" {
					t.Fatalf("valid control failed: %v", score)
				}
			} else if report != nil || score["status"] != "passed" || score["report_valid"] != false || score["report_error"] == nil {
				t.Fatalf("grounded claims overrode %s failure: %v", failure, score)
			}
		})
	}
}
