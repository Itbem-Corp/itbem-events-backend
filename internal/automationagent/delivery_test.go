package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseDeliveryPlanRequiresEveryHumanReviewField(t *testing.T) {
	valid := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"changes","notes":"Bounded change"}],"files_impacted":["controllers/delivery"],"rollback_plan":["revert the bounded diff"],"estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(valid)
	if err != nil || plan["summary"] != "A bounded plan" {
		t.Fatalf("unexpected plan: %#v, %v", plan, err)
	}
	impact, ok := plan["repository_impact"].([]any)
	if !ok || len(impact) != 1 || impact[0].(map[string]any)["impact"] != "changes" {
		t.Fatalf("repository impact matrix was not preserved: %#v", plan["repository_impact"])
	}
	if _, err := ParseDeliveryPlan(`{"summary":"missing fields"}`); err == nil {
		t.Fatal("expected incomplete plan rejection")
	}
}

func TestParseDeliveryPlanRequiresExecutionContractWhenChangingCode(t *testing.T) {
	valid := map[string]any{
		"summary": "A bounded plan", "goal_interpretation": "Deliver the bounded change", "confidence": 0.8,
		"autonomy_boundary": "Wait for human gates.", "context_reviewed": []any{"repo"}, "context_gaps": []any{},
		"assumptions": []any{}, "human_decisions": []any{}, "implementation_steps": []any{"change"},
		"risks": []any{}, "qa_plan": []any{"test"}, "evidence_plan": []any{"report"},
		"acceptance_criteria": []any{"works"}, "repository_impact": []any{map[string]any{
			"name": "Backend", "reference": "workspace://repo", "revision": "deadbeef", "role": "primary", "impact": "changes", "notes": "Bounded change",
		}}, "files_impacted": []any{"controllers/delivery.go"}, "rollback_plan": []any{"revert"}, "estimate": "30 minutes", "questions": []any{},
	}
	for _, field := range []string{"implementation_steps", "qa_plan", "evidence_plan", "acceptance_criteria", "rollback_plan"} {
		t.Run(field, func(t *testing.T) {
			rawValid, err := json.Marshal(valid)
			if err != nil {
				t.Fatal(err)
			}
			candidate := map[string]any{}
			if err := json.Unmarshal(rawValid, &candidate); err != nil {
				t.Fatal(err)
			}
			candidate[field] = []any{}
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseDeliveryPlan(string(raw)); err == nil {
				t.Fatalf("expected code-changing plan without %s to fail", field)
			}
		})
	}
}

func TestParseDeliveryPlanAllowsEmptyExecutionListsWhileExploratory(t *testing.T) {
	plan := map[string]any{
		"summary": "Need context", "goal_interpretation": "Clarify scope", "confidence": 0.2,
		"autonomy_boundary": "No changes before human review.", "context_reviewed": []any{}, "context_gaps": []any{"Repository is missing"},
		"assumptions": []any{}, "human_decisions": []any{}, "implementation_steps": []any{}, "risks": []any{},
		"qa_plan": []any{}, "evidence_plan": []any{}, "acceptance_criteria": []any{}, "repository_impact": []any{},
		"files_impacted": []any{}, "rollback_plan": []any{}, "estimate": "Unknown", "questions": []any{},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDeliveryPlan(string(raw)); err != nil {
		t.Fatalf("exploratory plan should remain reviewable: %v", err)
	}
}

func TestDecodeJSONObjectRepairsOnlyOneTrailingArrayClosure(t *testing.T) {
	decoded, ok := decodeJSONObject(`{"summary":"bounded","items":["one"}`)
	if !ok || decoded["summary"] != "bounded" {
		t.Fatalf("expected deterministic trailing array closure recovery, got %#v", decoded)
	}
	if _, ok := decodeJSONObject(`{"items":[{"summary":"nested"}`); ok {
		t.Fatal("a malformed root must not be replaced with a nested object")
	}
	if _, ok := decodeJSONObject(`{"items":["one"}}`); ok {
		t.Fatal("unexpected or mismatched delimiters must remain rejected")
	}
}

func TestParseDeliveryPlanNormalizesAProviderRollbackSentence(t *testing.T) {
	valid := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"}],"files_impacted":["controllers/delivery"],"rollback_plan":"No mutation occurred.","estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(valid)
	if err != nil {
		t.Fatal(err)
	}
	if rollback, ok := plan["rollback_plan"].([]any); !ok || len(rollback) != 1 || rollback[0] != "No mutation occurred." {
		t.Fatalf("rollback sentence was not normalized: %#v", plan["rollback_plan"])
	}
}

func TestParseDeliveryPlanMakesOmittedRisksExplicitlyReviewable(t *testing.T) {
	valid := `{"summary":"An exploratory plan","goal_interpretation":"Validate the local worker","confidence":0.72,"autonomy_boundary":"Plan only; wait for a human gate.","context_reviewed":["repo"],"context_gaps":["Worker health is not yet observed"],"assumptions":[],"human_decisions":[],"implementation_steps":["Observe the worker"],"qa_plan":["Check the heartbeat"],"evidence_plan":["Persist the run id"],"acceptance_criteria":["No code changes"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"}],"files_impacted":[],"rollback_plan":["Stop the local worker"],"estimate":"10 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(valid)
	if err != nil {
		t.Fatal(err)
	}
	risks, ok := plan["risks"].([]any)
	if !ok || len(risks) != 1 || !strings.Contains(fmt.Sprint(risks[0]), "omitted") {
		t.Fatalf("expected an explicit review risk, got %#v", plan["risks"])
	}
	repairs, ok := plan["_harness_repairs"].([]any)
	if !ok || len(repairs) != 1 || !strings.Contains(fmt.Sprint(repairs[0]), "risks missing") {
		t.Fatalf("expected an observable repair marker, got %#v", plan["_harness_repairs"])
	}
}

func TestParseDeliveryPlanNormalizesBoundedRiskSentence(t *testing.T) {
	valid := `{"summary":"An exploratory plan","goal_interpretation":"Validate the local worker","confidence":0.72,"autonomy_boundary":"Plan only; wait for a human gate.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["Observe the worker"],"risks":"The worker may be offline.","qa_plan":["Check the heartbeat"],"evidence_plan":["Persist the run id"],"acceptance_criteria":["No code changes"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"}],"files_impacted":[],"rollback_plan":["Stop the local worker"],"estimate":"10 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(valid)
	if err != nil {
		t.Fatal(err)
	}
	risks, ok := plan["risks"].([]any)
	if !ok || len(risks) != 1 || risks[0] != "The worker may be offline." {
		t.Fatalf("expected normalized risk list, got %#v", plan["risks"])
	}
}

func TestParseDeliveryPlanNormalizesProviderBrowserActionAlias(t *testing.T) {
	valid := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"}],"files_impacted":[],"rollback_plan":["No mutation occurred."],"estimate":"30 minutes","questions":[],"browser_qa_mode":"read_only","browser_qa_cases":[{"id":"root","title":"Open root","steps":[{"action":"navigate","path":"/"},{"action":"assert_text","text":"Welcome"}]}]}`
	plan, err := ParseDeliveryPlan(valid)
	if err != nil {
		t.Fatal(err)
	}
	step := plan["browser_qa_cases"].([]any)[0].(map[string]any)["steps"].([]any)[0].(map[string]any)
	if step["kind"] != "navigate" {
		t.Fatalf("provider action alias was not canonicalized: %#v", step)
	}
	if _, exists := step["action"]; exists {
		t.Fatalf("provider-only alias must not persist in the executable plan: %#v", step)
	}
}

func TestParseDeliveryPlanRetainsHumanGateWhenOptionalBrowserProposalIsInvalid(t *testing.T) {
	valid := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"}],"files_impacted":[],"rollback_plan":["No mutation occurred."],"estimate":"30 minutes","questions":[],"browser_qa_mode":"approved_test_flow","browser_qa_cases":[{"id":"login","title":"Login","steps":[{"kind":"click","selector":"button[type=submit]"}]}]}`
	plan, err := ParseDeliveryPlan(valid)
	if err != nil {
		t.Fatalf("an optional malformed browser proposal must not discard a reviewable plan: %v", err)
	}
	if _, exists := plan["browser_qa_mode"]; exists {
		t.Fatalf("an invalid browser mode must not reach the approval boundary: %#v", plan)
	}
	review, ok := plan["browser_qa_review"].(map[string]any)
	if !ok || review["status"] != "requires_human_revision" {
		t.Fatalf("reviewer must be told that browser QA needs revision: %#v", plan["browser_qa_review"])
	}
	decisions := plan["human_decisions"].([]any)
	if len(decisions) != 1 || !strings.Contains(fmt.Sprint(decisions[0]), "navegador") {
		t.Fatalf("browser correction must become a human decision: %#v", decisions)
	}
}

func TestValidateApprovedBrowserQAPlanRejectsConflictingProviderAliases(t *testing.T) {
	plan := map[string]any{"browser_qa_cases": []any{map[string]any{
		"id": "root", "title": "Open root", "steps": []any{map[string]any{"kind": "navigate", "action": "assert_text", "path": "/"}},
	}}}
	if err := ValidateApprovedBrowserQAPlan(plan); err == nil {
		t.Fatal("conflicting browser action aliases must remain rejected")
	}
}

func TestValidateDeliveryPlanTopologyRejectsModelInventedRepositoriesAndRevisions(t *testing.T) {
	content := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"},{"name":"Dashboard","reference":"workspace://dashboard","revision":"cafebabe","role":"supporting","impact":"untouched","notes":"No change"}],"files_impacted":["controllers/delivery"],"rollback_plan":["revert the bounded diff"],"estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(content)
	if err != nil {
		t.Fatal(err)
	}
	topology := json.RawMessage(`{"repository_topology":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary"},{"name":"Dashboard","reference":"workspace://dashboard","revision":"cafebabe","role":"supporting"}]}`)
	if err := ValidateDeliveryPlanTopology(plan, topology); err != nil {
		t.Fatalf("expected exact frozen topology to be accepted: %v", err)
	}
	plan["repository_impact"].([]any)[1].(map[string]any)["revision"] = "different"
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("expected rewritten repository revision to be rejected")
	}
	plan["repository_impact"].([]any)[1].(map[string]any)["revision"] = "cafebabe"
	plan["repository_impact"] = plan["repository_impact"].([]any)[:1]
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("expected omitted supporting repository to be rejected")
	}
}

func TestValidateDeliveryPlanTopologyKeepsOperatorComponentScopeMaximum(t *testing.T) {
	content := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["evidence"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Dashboard","reference":"workspace://dashboard","revision":"cafebabe","role":"primary","impact":"changes","notes":"Bounded component","allowed_paths":["apps/dashboard/src"]}],"files_impacted":["apps/dashboard/src"],"rollback_plan":["revert"],"estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(content)
	if err != nil {
		t.Fatal(err)
	}
	topology := json.RawMessage(`{"repository_topology":[{"name":"Dashboard","reference":"workspace://dashboard","revision":"cafebabe","role":"primary","allowed_paths":["apps/dashboard"]}]}`)
	if err := ValidateDeliveryPlanTopology(plan, topology); err != nil {
		t.Fatalf("a narrowed component scope should remain valid: %v", err)
	}
	entry := plan["repository_impact"].([]any)[0].(map[string]any)
	entry["allowed_paths"] = []any{"packages/shared"}
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("a plan must not widen beyond the operator-approved component root")
	}
	delete(entry, "allowed_paths")
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("omitting a required component scope must fail closed")
	}
}

func TestValidateDeliveryPlanTopologyRejectsChangesToGitHubOnlyRepository(t *testing.T) {
	content := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":["The dashboard is metadata-only"],"assumptions":[],"human_decisions":[],"implementation_steps":["change backend"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://backend","revision":"deadbeef","role":"primary","impact":"changes","notes":"Local checkout available"},{"name":"Dashboard","reference":"github://Itbem-Corp/dashboard","revision":"cafebabe","role":"supporting","impact":"changes","notes":"Remote checkpoint"}],"files_impacted":["controllers/delivery"],"rollback_plan":["revert the bounded diff"],"estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(content)
	if err != nil {
		t.Fatal(err)
	}
	topology := json.RawMessage(`{"repository_topology":[{"name":"Backend","reference":"workspace://backend","revision":"deadbeef","role":"primary"},{"name":"Dashboard","reference":"github://Itbem-Corp/dashboard","revision":"cafebabe","role":"supporting"}]}`)
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("a github-only checkpoint must not become an implementation target")
	}
	plan["repository_impact"].([]any)[1].(map[string]any)["impact"] = "consulted"
	if err := ValidateDeliveryPlanTopology(plan, topology); err != nil {
		t.Fatalf("metadata-only repository should remain available for planning: %v", err)
	}
}

func TestDeliveryPlanQAExecutionMatrixBindsEveryFrozenRepository(t *testing.T) {
	content := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"},{"name":"Dashboard","reference":"workspace://dashboard","revision":"cafebabe","role":"supporting","impact":"untouched","notes":"No change"}],"qa_execution_matrix":[{"repository_ref":"workspace://repo","run_validation":true,"run_qa":true,"run_stagehand":false,"collect_evidence":true},{"repository_ref":"workspace://dashboard","run_validation":false,"run_qa":true,"run_stagehand":true,"collect_evidence":true}],"files_impacted":["controllers/delivery"],"rollback_plan":["revert the bounded diff"],"estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan(content)
	if err != nil {
		t.Fatal(err)
	}
	topology := json.RawMessage(`{"repository_topology":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary"},{"name":"Dashboard","reference":"workspace://dashboard","revision":"cafebabe","role":"supporting"}]}`)
	if err := ValidateDeliveryPlanTopology(plan, topology); err != nil {
		t.Fatalf("expected exact QA matrix to be accepted: %v", err)
	}
	matrix := plan["qa_execution_matrix"].([]any)
	matrix[1].(map[string]any)["repository_ref"] = "workspace://invented"
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("expected invented QA matrix repository to be rejected")
	}
	matrix[1].(map[string]any)["repository_ref"] = "workspace://dashboard"
	plan["qa_execution_matrix"] = matrix[:1]
	if err := ValidateDeliveryPlanTopology(plan, topology); err == nil {
		t.Fatal("expected omitted QA matrix repository to be rejected")
	}
}

func TestParseDeliveryPlanRejectsMalformedQAExecutionMatrix(t *testing.T) {
	plan := map[string]any{
		"qa_execution_matrix": []any{map[string]any{
			"repository_ref": "workspace://repo", "run_validation": true,
			"run_qa": true, "run_stagehand": "yes", "collect_evidence": true,
		}},
	}
	if err := normalizeQAExecutionMatrix(plan); err == nil {
		t.Fatal("QA execution matrix must reject non-boolean capabilities")
	}
	plan["qa_execution_matrix"] = []any{map[string]any{
		"repository_ref": "workspace://repo", "run_validation": false,
		"run_qa": true, "run_stagehand": true, "collect_evidence": false,
	}}
	if err := normalizeQAExecutionMatrix(plan); err == nil {
		t.Fatal("Stagehand must require visual evidence in the approved QA contract")
	}
}

func TestValidateDeliveryPlanContextCoverageRequiresEveryFrozenSource(t *testing.T) {
	plan := map[string]any{"context_reviewed": []any{"workspace://backend", "document://brief-v2", "conversation://client/42"}}
	delivery := json.RawMessage(`{"context_sources":[{"reference":"workspace://backend"},{"reference":"document://brief-v2"},{"reference":"conversation://client/42"}]}`)
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err != nil {
		t.Fatalf("expected exact frozen context coverage: %v", err)
	}
	plan["context_reviewed"] = []any{"workspace://backend", "document://brief-v2"}
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err == nil {
		t.Fatal("omitted client context must retain the plan for review")
	}
	plan["context_reviewed"] = []any{"workspace://backend", "document://brief-v2", "conversation://invented"}
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err == nil {
		t.Fatal("invented context reference must be rejected")
	}
}

func TestValidateDeliveryPlanContextCoverageNormalizesExactFrozenProvenance(t *testing.T) {
	plan := map[string]any{"context_reviewed": []any{
		"workspace://backend@deadbeef (snapshot_at 2026-08-10T04:55:51Z)",
		"document://brief-v2@cafebabe",
	}}
	delivery := json.RawMessage(`{"context_sources":[{"reference":"workspace://backend","revision":"deadbeef","snapshot_at":"2026-08-10T04:55:51Z"},{"reference":"document://brief-v2","revision":"cafebabe"}]}`)
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err != nil {
		t.Fatalf("expected exact decorated frozen provenance to normalize: %v", err)
	}
	if got := plan["context_reviewed"].([]any); got[0] != "workspace://backend" || got[1] != "document://brief-v2" {
		t.Fatalf("expected canonical source references, got %#v", got)
	}
	plan["context_reviewed"] = []any{"workspace://backend@different"}
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err == nil {
		t.Fatal("wrong revision must never be normalized")
	}
}

func TestValidateDeliveryPlanContextCoverageNormalizesFixedRepositoryMarker(t *testing.T) {
	plan := map[string]any{"context_reviewed": []any{"github://Itbem-Corp/itbem-events-backend@1110ee62f34d648316390212ebf558ef3cd9bef8 (repository)"}}
	delivery := json.RawMessage(`{"context_sources":[{"reference":"github://Itbem-Corp/itbem-events-backend","revision":"1110ee62f34d648316390212ebf558ef3cd9bef8"}]}`)
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err != nil {
		t.Fatalf("expected fixed repository marker to normalize: %v", err)
	}
	if got := plan["context_reviewed"].([]any); got[0] != "github://Itbem-Corp/itbem-events-backend" {
		t.Fatalf("expected canonical repository reference, got %#v", got)
	}

	plan["context_reviewed"] = []any{"github://Itbem-Corp/itbem-events-backend@1110ee62f34d648316390212ebf558ef3cd9bef8 (untrusted)"}
	if err := ValidateDeliveryPlanContextCoverage(plan, delivery); err == nil {
		t.Fatal("arbitrary provenance prose must remain rejected")
	}

	plan["context_reviewed"] = []any{"document://architecture@1110ee62f34d648316390212ebf558ef3cd9bef8 (repository)"}
	documentDelivery := json.RawMessage(`{"context_sources":[{"reference":"document://architecture","revision":"1110ee62f34d648316390212ebf558ef3cd9bef8"}]}`)
	if err := ValidateDeliveryPlanContextCoverage(plan, documentDelivery); err == nil {
		t.Fatal("the repository marker must not relabel a non-repository source")
	}
}

func TestValidateDeliveryPlanTopologyRequiresStagehandEvidenceForConfiguredFrontend(t *testing.T) {
	plan := map[string]any{
		"repository_impact": []any{map[string]any{
			"name": "Dashboard", "reference": "workspace://dashboard", "revision": "deadbeef",
			"role": "primary", "impact": "consulted", "notes": "Planning only",
		}},
		"qa_execution_matrix": []any{map[string]any{
			"repository_ref": "workspace://dashboard", "run_validation": true,
			"run_qa": true, "run_stagehand": false, "collect_evidence": true,
		}},
	}
	delivery := json.RawMessage(`{"repository_topology":[{"name":"Dashboard","reference":"workspace://dashboard","revision":"deadbeef","role":"primary","kind":"frontend","stagehand_configured":true}]}`)
	if err := ValidateDeliveryPlanTopology(plan, delivery); err == nil {
		t.Fatal("configured frontend must require Stagehand evidence in its QA matrix")
	}
	plan["qa_execution_matrix"] = []any{map[string]any{
		"repository_ref": "workspace://dashboard", "run_validation": true,
		"run_qa": true, "run_stagehand": true, "collect_evidence": true,
	}}
	if err := ValidateDeliveryPlanTopology(plan, delivery); err == nil {
		t.Fatal("configured frontend must require a concrete browser E2E case, not only a QA matrix flag")
	}
	plan["browser_qa_mode"] = "read_only"
	plan["browser_qa_cases"] = []any{map[string]any{
		"id": "landing", "title": "Verify the delivery landing page", "steps": []any{
			map[string]any{"kind": "navigate", "path": "/"},
			map[string]any{"kind": "assert_visible", "selector": "main"},
		},
	}}
	if err := ValidateDeliveryPlanTopology(plan, delivery); err != nil {
		t.Fatalf("expected approved Stagehand evidence contract: %v", err)
	}
}

func TestValidateDeliveryPlanTopologyRequiresStagehandForEveryConfiguredFrontend(t *testing.T) {
	plan := map[string]any{
		"repository_impact": []any{
			map[string]any{"name": "Dashboard", "reference": "workspace://dashboard", "revision": "deadbeef", "role": "primary", "impact": "consulted", "notes": "Planning only"},
			map[string]any{"name": "Portal", "reference": "workspace://portal", "revision": "cafebabe", "role": "supporting", "impact": "consulted", "notes": "Planning only"},
		},
		"qa_execution_matrix": []any{
			map[string]any{"repository_ref": "workspace://dashboard", "run_validation": true, "run_qa": true, "run_stagehand": true, "collect_evidence": true},
			map[string]any{"repository_ref": "workspace://portal", "run_validation": true, "run_qa": true, "run_stagehand": false, "collect_evidence": true},
		},
		"browser_qa_mode":  "read_only",
		"browser_qa_cases": []any{map[string]any{"id": "landing", "title": "Verify landing", "steps": []any{map[string]any{"kind": "navigate", "path": "/"}}}},
	}
	delivery := json.RawMessage(`{"repository_topology":[{"name":"Dashboard","reference":"workspace://dashboard","revision":"deadbeef","role":"primary","kind":"frontend","stagehand_configured":true},{"name":"Portal","reference":"workspace://portal","revision":"cafebabe","role":"supporting","kind":"frontend","stagehand_configured":true}]}`)
	if err := ValidateDeliveryPlanTopology(plan, delivery); err == nil {
		t.Fatal("every configured frontend must receive Stagehand evidence")
	}
}

func TestParseDeliveryPlanAcceptsFencedJSONObjectButNotProse(t *testing.T) {
	valid := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change with { braces }"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"consulted","notes":"Planning only"}],"files_impacted":["controllers/delivery"],"rollback_plan":["revert the bounded diff"],"estimate":"30 minutes","questions":[]}`
	plan, err := ParseDeliveryPlan("Here is the requested plan:\n```json\n" + valid + "\n```")
	if err != nil || plan["summary"] != "A bounded plan" {
		t.Fatalf("fenced plan rejected: %#v, %v", plan, err)
	}
	if _, err := ParseDeliveryPlan("A prose-only response is not a plan."); err == nil {
		t.Fatal("expected prose response rejection")
	}
}

func TestParseDeliveryPlanAcceptsAProviderEscapedJSONObject(t *testing.T) {
	valid := `{"summary":"A bounded plan","goal_interpretation":"Deliver the bounded change","confidence":0.8,"autonomy_boundary":"Propose only; wait for human gates.","context_reviewed":["repo"],"context_gaps":[],"assumptions":[],"human_decisions":[],"implementation_steps":["change"],"risks":[],"qa_plan":["test"],"evidence_plan":["screenshot"],"acceptance_criteria":["works"],"repository_impact":[{"name":"Backend","reference":"workspace://repo","revision":"deadbeef","role":"primary","impact":"untouched","notes":"No change"}],"files_impacted":["controllers/delivery"],"rollback_plan":["revert the bounded diff"],"estimate":"30 minutes","questions":[]}`
	wrapper, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ParseDeliveryPlan(string(wrapper))
	if err != nil || plan["summary"] != "A bounded plan" {
		t.Fatalf("escaped provider plan rejected: %#v, %v", plan, err)
	}
}

func TestParseDeliverySummaryRequiresAReviewableReleaseDraft(t *testing.T) {
	valid := `{"executive":{"what_changed":"Delivery flow","why":"Reduce review friction","how_to_test":"Open the QA evidence","risks":["Human release gate remains required"]},"technical":{"decisions":["Keep the human approval"],"evidence":["QA screenshot: abc123"]}}`
	summary, err := ParseDeliverySummary(valid)
	if err != nil || summary["executive"].(map[string]any)["what_changed"] != "Delivery flow" {
		t.Fatalf("valid delivery summary rejected: %#v / %v", summary, err)
	}
	for _, invalid := range []string{
		`{"executive":{"what_changed":"x","why":"y","how_to_test":"z","risks":[]},"technical":{"decisions":["d"],"evidence":["e"]}}`,
		`{"executive":{"what_changed":"x","why":"y","how_to_test":"z","risks":["r"]},"technical":{"decisions":["d"]}}`,
		"prose only",
	} {
		if _, err := ParseDeliverySummary(invalid); err == nil {
			t.Fatalf("invalid delivery draft must fail closed: %s", invalid)
		}
	}
}

func TestParseDeliverySummaryRepairsStructuredEvidenceCitations(t *testing.T) {
	content := `{"executive":{"what_changed":"Delivery flow","why":"Reduce review friction","how_to_test":"Open the QA evidence","risks":["Human release gate remains required"]},"technical":{"decisions":[],"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test","summary":"Admin and non-admin paths were observed."}]}}`
	summary, err := ParseDeliverySummary(content)
	if err != nil {
		t.Fatal(err)
	}
	technical := summary["technical"].(map[string]any)
	evidence := technical["evidence"].([]any)
	if len(evidence) != 1 || evidence[0] != "1e5c5eb5-38cb-46af-9e50-a196ad7fc333 — Synthetic authorization test: Admin and non-admin paths were observed." {
		t.Fatalf("structured evidence was not normalized: %#v", evidence)
	}
	repairs := technical["_harness_repairs"].([]any)
	if len(repairs) != 1 {
		t.Fatalf("expected one observable bounded repair, got %#v", repairs)
	}
	delivery := json.RawMessage(`{"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test"}],"gates":[]}`)
	if err := ValidateDeliverySummaryEvidence(summary, delivery); err != nil {
		t.Fatalf("normalized citation should still pass membership validation: %v", err)
	}
}

func TestValidateDeliverySummaryGroundsHumanDecisions(t *testing.T) {
	content := `{"executive":{"what_changed":"Delivery flow","why":"Reduce review friction","how_to_test":"Open the QA evidence","risks":["Human release gate remains required"]},"technical":{"decisions":["Plan gate approved by reviewer"],"evidence":["1e5c5eb5-38cb-46af-9e50-a196ad7fc333 — Synthetic authorization test"]}}`
	summary, err := ParseDeliverySummary(content)
	if err != nil {
		t.Fatal(err)
	}
	delivery := json.RawMessage(`{"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test"}],"gates":[{"kind":"plan","decision":"approved"}]}`)
	if err := ValidateDeliverySummaryEvidence(summary, delivery); err != nil {
		t.Fatalf("grounded gate decision rejected: %v", err)
	}
	for _, invalid := range []struct {
		name     string
		content  string
		delivery string
	}{
		{"generic decision", `{"executive":{"what_changed":"x","why":"y","how_to_test":"z","risks":["r"]},"technical":{"decisions":["Reviewed"],"evidence":["1e5c5eb5-38cb-46af-9e50-a196ad7fc333 — Synthetic authorization test"]}}`, `{"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test"}],"gates":[{"kind":"plan","decision":"approved"}]}`},
		{"invented without gate", `{"executive":{"what_changed":"x","why":"y","how_to_test":"z","risks":["r"]},"technical":{"decisions":["Plan approved"],"evidence":["1e5c5eb5-38cb-46af-9e50-a196ad7fc333 — Synthetic authorization test"]}}`, `{"evidence":[{"id":"1e5c5eb5-38cb-46af-9e50-a196ad7fc333","title":"Synthetic authorization test"}],"gates":[]}`},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			candidate, err := ParseDeliverySummary(invalid.content)
			if err != nil {
				t.Fatal(err)
			}
			if ValidateDeliverySummaryEvidence(candidate, json.RawMessage(invalid.delivery)) == nil {
				t.Fatal("ungrounded human decision was accepted")
			}
		})
	}
}

func TestParseDeliveryQAReportAndObservedFailureValidation(t *testing.T) {
	valid := `{"summary":"Preview and checks passed","verdict":"passed","checks":[{"name":"Preview","status":"passed","detail":"HTTP 200"}],"defects":[],"coverage_gaps":[],"recommended_actions":["Human QA review"]}`
	report, err := ParseDeliveryQAReport(valid)
	if err != nil || report["verdict"] != "passed" {
		t.Fatalf("valid QA report rejected: %#v / %v", report, err)
	}
	if err := ValidateDeliveryQAReport(report, map[string]any{"preview": map[string]any{"passed": true}, "repository_runs": []any{map[string]any{"commands": []any{map[string]any{"passed": true}}}}}); err != nil {
		t.Fatalf("passing observed execution rejected: %v", err)
	}
	if err := ValidateDeliveryQAReport(report, map[string]any{"preview": map[string]any{"passed": false}}); err == nil {
		t.Fatal("report must not describe a failed observed preview as passed")
	}
	if err := ValidateDeliveryQAReport(report, map[string]any{"preview": map[string]any{"passed": true}, "semantic": map[string]any{"passed": false}}); err == nil {
		t.Fatal("report must not describe a failed semantic browser check as passed")
	}
	for _, invalid := range []string{
		`{"summary":"x","verdict":"passed","checks":[],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`,
		`{"summary":"x","verdict":"released","checks":[{"name":"x","status":"passed","detail":"x"}],"defects":[],"coverage_gaps":[],"recommended_actions":[]}`,
	} {
		if _, err := ParseDeliveryQAReport(invalid); err == nil {
			t.Fatalf("invalid QA report was accepted: %s", invalid)
		}
	}
}

func TestParseChangeProposalRejectsSensitivePaths(t *testing.T) {
	valid := `{"summary":"bounded","patch":"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1 @@\n+ok"}`
	if _, err := ParseChangeProposal(valid); err != nil {
		t.Fatal(err)
	}
	unsafe := `{"summary":"bad","patch":"diff --git a/.env b/.env\n--- a/.env\n+++ b/.env\n@@ -0,0 +1 @@\n+x"}`
	if _, err := ParseChangeProposal(unsafe); err == nil {
		t.Fatal("expected sensitive path rejection")
	}
	nested := `{"summary":"bad","patch":"diff --git a/config/.env.local b/config/.env.local\n--- a/config/.env.local\n+++ b/config/.env.local\n@@ -0,0 +1 @@\n+x"}`
	if _, err := ParseChangeProposal(nested); err == nil {
		t.Fatal("expected nested sensitive path rejection")
	}
}

func TestRepositoryCommandEnvironmentStripsWorkerSecretsButAllowsExplicitEphemeralCredential(t *testing.T) {
	inherited := []string{
		"PATH=C:\\tools",
		"ITBEM_AI_PROVIDER_API_KEY=must-not-reach-repository",
		"MINIMAX_API_KEY=must-not-reach-repository",
		"GITHUB_TOKEN=must-not-reach-repository",
		"AWS_SECRET_ACCESS_KEY=must-not-reach-repository",
		"DATABASE_URL=must-not-reach-repository",
		"SAFE_FLAG=enabled",
	}
	environment := repositoryCommandEnvironment(inherited, map[string]string{"ITBEM_GITHUB_INSTALLATION_TOKEN": "short-lived", "GIT_ASKPASS": "C:\\Temp\\askpass.cmd"})
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	for _, forbidden := range []string{"ITBEM_AI_PROVIDER_API_KEY=", "MINIMAX_API_KEY=", "GITHUB_TOKEN=", "AWS_SECRET_ACCESS_KEY=", "DATABASE_URL="} {
		if strings.Contains(joined, "\n"+forbidden) {
			t.Fatalf("repository child inherited a worker secret: %s in %q", forbidden, joined)
		}
	}
	for _, expected := range []string{"PATH=C:\\tools", "SAFE_FLAG=enabled", "ITBEM_GITHUB_INSTALLATION_TOKEN=short-lived", "GIT_ASKPASS=C:\\Temp\\askpass.cmd"} {
		if !strings.Contains(joined, "\n"+expected+"\n") {
			t.Fatalf("expected child environment entry missing: %s in %q", expected, joined)
		}
	}
}

func TestPublicationPushArgumentsDisableHooksAndCredentialHelpers(t *testing.T) {
	arguments := publicationPushArguments("https://github.com/Itbem-Corp/example.git", "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb")
	joined := strings.Join(arguments, " ")
	for _, expected := range []string{"credential.helper=", "push", "--no-verify", "HEAD:refs/heads/itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("publication must use the hardened git argument %q: %#v", expected, arguments)
		}
	}
}

func TestParseChangeProposalBindsEachMultiRepositoryPatchToAWorkspace(t *testing.T) {
	content := `{"summary":"bounded","patches":[{"repository_ref":"workspace://api","patch":"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1 @@\n+api"},{"repository_ref":"workspace://web","patch":"diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -0,0 +1 @@\n+web"}]}`
	proposal, err := ParseChangeProposal(content)
	if err != nil || len(proposal.Patches) != 2 || proposal.Patches[0].RepositoryRef != "workspace://api" || proposal.Patch != "" {
		t.Fatalf("expected explicit multi-repository proposal: %#v / %v", proposal, err)
	}
	for _, invalid := range []string{
		`{"summary":"mixed","patch":"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1 @@\n+x","patches":[{"repository_ref":"workspace://api","patch":"diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -0,0 +1 @@\n+x"}]}`,
		`{"summary":"duplicate","patches":[{"repository_ref":"workspace://api","patch":"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1 @@\n+x"},{"repository_ref":"workspace://api","patch":"diff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -0,0 +1 @@\n+x"}]}`,
	} {
		if _, err := ParseChangeProposal(invalid); err == nil {
			t.Fatalf("unsafe multi-repository proposal must fail: %s", invalid)
		}
	}
}

func TestRepositoryComponentScopeRejectsPatchOutsideApprovedMonorepoPath(t *testing.T) {
	paths, err := NormalizeRepositoryAllowedPaths([]any{"apps/web", "packages/ui"})
	if err != nil || len(paths) != 2 || paths[0] != "apps/web" {
		t.Fatalf("expected normalized component roots: %#v / %v", paths, err)
	}
	patch := "diff --git a/apps/web/page.tsx b/apps/web/page.tsx\n--- a/apps/web/page.tsx\n+++ b/apps/web/page.tsx\n@@ -1 +1 @@\n-old\n+new"
	if err := validatePatchPaths(patch, paths); err != nil {
		t.Fatalf("patch inside approved component roots rejected: %v", err)
	}
	unsafe := strings.ReplaceAll(patch, "apps/web/page.tsx", "services/api/main.go")
	if err := validatePatchPaths(unsafe, paths); err == nil {
		t.Fatal("patch outside approved component roots must fail closed")
	}
	for _, raw := range []any{[]any{"../escape"}, []any{".env"}, []any{"/absolute"}, []any{"apps/web", "apps/web"}} {
		if _, err := NormalizeRepositoryAllowedPaths(raw); err == nil {
			t.Fatalf("unsafe or duplicate component scope accepted: %#v", raw)
		}
	}
}

func TestNormalizeUnifiedPatchHunkCountsChangesOnlyTheHeader(t *testing.T) {
	patch := "diff --git a/docs/example.md b/docs/example.md\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/docs/example.md\n@@ -0,0 +1,99 @@\n+uno\n+dos\n"
	normalized, changed := normalizeUnifiedPatchHunkCounts(patch)
	if !changed {
		t.Fatal("expected malformed hunk count normalization")
	}
	if !strings.Contains(normalized, "@@ -0,0 +1,2 @@") || !strings.Contains(normalized, "+uno\n+dos") {
		t.Fatalf("unexpected normalized patch: %q", normalized)
	}
}

func TestRunImplementationUsesIsolatedWorktree(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}, {"git", "remote", "add", "origin", "https://github.com/Itbem-Corp/test-repo.git"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v, %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "note.txt"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git commit failed: %#v, %v", result, err)
		}
	}
	initial, err := runLocal(context.Background(), root, commandTimeout, "", "git", "rev-parse", "HEAD")
	if err != nil || initial.ExitCode != 0 || !gitCommitPattern.MatchString(strings.TrimSpace(initial.Output)) {
		t.Fatalf("could not capture frozen base revision: %#v, %v", initial, err)
	}
	frozenRevision := strings.TrimSpace(initial.Output)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("newer base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "note.txt"}, {"git", "commit", "-m", "advance base"}} {
		result, runErr := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if runErr != nil || result.ExitCode != 0 {
			t.Fatalf("git base advance failed: %#v, %v", result, runErr)
		}
	}
	workspaceJSON := `{"repo":{"path":"` + filepath.ToSlash(root) + `"}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return workspaceJSON
		}
		return ""
	}
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://repo","revision":"` + frozenRevision + `"}]}`)
	proposal := `{"summary":"change note","patch":"diff --git a/note.txt b/note.txt\n--- a/note.txt\n+++ b/note.txt\n@@ -1 +1 @@\n-before\n+after\ndiff --git a/new.txt b/new.txt\nnew file mode 100644\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+new file\n"}`
	taskID := "d4a4b837-2e18-43af-9f58-6d59629db2bb"
	result, err := RunImplementation(context.Background(), taskID, delivery, proposal, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if result["branch"] != "itbem-agent/"+taskID {
		t.Fatalf("unexpected implementation result: %#v", result)
	}
	baseSHA, _ := result["base_sha"].(string)
	reviewDigest, _ := result["review_diff_sha256"].(string)
	if !gitCommitPattern.MatchString(baseSHA) || !sha256DigestPattern.MatchString(reviewDigest) {
		t.Fatalf("implementation must return an immutable reviewed diff digest: %#v", result)
	}
	if baseSHA != frozenRevision {
		t.Fatalf("implementation must create its worktree at the frozen SHA, got %s want %s", baseSHA, frozenRevision)
	}
	worktree := filepath.Join(root, ".itbem-agent-worktrees", taskID)
	if result["github_repository"] != "Itbem-Corp/test-repo" {
		t.Fatalf("implementation must bind the reviewed GitHub origin: %#v", result)
	}
	auth := &publicationAuthorization{GrantID: taskID, RepositoryRef: "workspace://repo", BaseSHA: baseSHA, GitHubRepository: "Itbem-Corp/test-repo", ReviewDiffSHA256: reviewDigest, Branch: "itbem-agent/" + taskID, Capabilities: []string{"commit:stage", "branch:publish"}, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	if err := verifyReviewedWorktree(context.Background(), worktree, auth); err != nil {
		t.Fatalf("unchanged reviewed worktree should verify: %v", err)
	}
	if created, err := os.ReadFile(filepath.Join(worktree, "new.txt")); err != nil || string(created) != "new file\n" && string(created) != "new file\r\n" {
		t.Fatalf("review digest must include created files: %q, %v", created, err)
	}
	content, err := os.ReadFile(filepath.Join(root, ".itbem-agent-worktrees", taskID, "note.txt"))
	if err != nil || string(content) != "after\n" && string(content) != "after\r\n" {
		t.Fatalf("patch was not confined and applied in worktree: %q, %v", content, err)
	}
	base, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(base) != "newer base\n" && string(base) != "newer base\r\n" {
		t.Fatalf("implementation must not move or modify the newer base workspace: %q, %v", base, err)
	}
	if state := ReadWorkspaceGitState(Workspace{Root: root}); !state.Available || state.HasLocalChanges {
		t.Fatalf("an agent worktree must not make its dedicated base look dirty: %#v", state)
	}
	if err := os.WriteFile(filepath.Join(worktree, "note.txt"), []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReviewedWorktree(context.Background(), worktree, auth); err == nil {
		t.Fatal("publication must reject a worktree modified after human review")
	}
}

func TestIsolatedWorktreeAtRejectsInvalidAndUnavailableFrozenRevisions(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git commit failed: %#v / %v", result, err)
		}
	}
	workspace := Workspace{ID: "repo", Root: root}
	taskID := "d4a4b837-2e18-43af-9f58-6d59629db2bb"
	if _, _, err := isolatedWorktreeAt(context.Background(), workspace, taskID, "short-sha"); err == nil || !strings.Contains(err.Error(), "expected revision is invalid") {
		t.Fatalf("abbreviated revision must be rejected: %v", err)
	}
	if _, _, err := isolatedWorktreeAt(context.Background(), workspace, taskID, strings.Repeat("f", 40)); err == nil || !strings.Contains(err.Error(), "expected revision is unavailable") {
		t.Fatalf("unavailable full revision must be rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".itbem-agent-worktrees", taskID)); !os.IsNotExist(err) {
		t.Fatalf("invalid revisions must not create an isolated worktree: %v", err)
	}
}

func TestRunImplementationCreatesIndependentWorktreesForEveryChangedRepository(t *testing.T) {
	apiRoot, webRoot := filepath.Join(t.TempDir(), "api"), filepath.Join(t.TempDir(), "web")
	for _, repository := range []string{apiRoot, webRoot} {
		if err := os.MkdirAll(repository, 0700); err != nil {
			t.Fatal(err)
		}
		for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
			result, err := runLocal(context.Background(), repository, commandTimeout, "", command[0], command[1:]...)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("git setup failed for %s: %#v, %v", repository, result, err)
			}
		}
		if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := runLocal(context.Background(), repository, commandTimeout, "", "git", "add", "README.md")
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git add failed: %#v, %v", result, err)
		}
		result, err = runLocal(context.Background(), repository, commandTimeout, "", "git", "commit", "-m", "initial")
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git commit failed: %#v, %v", result, err)
		}
	}
	workspaceJSON := `{"api":{"path":"` + filepath.ToSlash(apiRoot) + `"},"web":{"path":"` + filepath.ToSlash(webRoot) + `"}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return workspaceJSON
		}
		return ""
	}
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://api"},{"kind":"repository","reference":"workspace://web"}],"repository_topology":[{"reference":"workspace://api"},{"reference":"workspace://web","depends_on":["workspace://api"]}],"approved_plan":{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://web","impact":"changes"}]}}`)
	proposal := `{"summary":"two repos","patches":[{"repository_ref":"workspace://api","patch":"diff --git a/api.txt b/api.txt\nnew file mode 100644\n--- /dev/null\n+++ b/api.txt\n@@ -0,0 +1 @@\n+api\n"},{"repository_ref":"workspace://web","patch":"diff --git a/web.txt b/web.txt\nnew file mode 100644\n--- /dev/null\n+++ b/web.txt\n@@ -0,0 +1 @@\n+web\n"}]}`
	taskID := "e4a4b837-2e18-43af-9f58-6d59629db2bb"
	result, err := RunImplementation(context.Background(), taskID, delivery, proposal, lookup)
	if err != nil {
		t.Fatal(err)
	}
	changeSets, ok := result["change_sets"].([]any)
	if !ok || len(changeSets) != 2 {
		t.Fatalf("expected two independently reviewable change sets: %#v", result)
	}
	if order, ok := result["repository_execution_order"].([]string); !ok || !reflect.DeepEqual(order, []string{"workspace://api", "workspace://web"}) {
		t.Fatalf("implementation must retain its dependency-first execution order: %#v", result)
	}
	for _, check := range []struct{ root, file, want string }{{apiRoot, "api.txt", "api"}, {webRoot, "web.txt", "web"}} {
		worktreeFile := filepath.Join(check.root, ".itbem-agent-worktrees", taskID, check.file)
		content, readErr := os.ReadFile(worktreeFile)
		if readErr != nil || strings.TrimSpace(string(content)) != check.want {
			t.Fatalf("worktree did not retain its isolated repository patch: %s / %q / %v", worktreeFile, content, readErr)
		}
		if _, readErr := os.Stat(filepath.Join(check.root, check.file)); !os.IsNotExist(readErr) {
			t.Fatalf("base repository was modified by a multi-repository implementation: %s / %v", check.root, readErr)
		}
	}
	if _, err := RunImplementation(context.Background(), "f4a4b837-2e18-43af-9f58-6d59629db2bb", delivery, `{"summary":"partial","patch":"diff --git a/partial.txt b/partial.txt\nnew file mode 100644\n--- /dev/null\n+++ b/partial.txt\n@@ -0,0 +1 @@\n+partial\n"}`, lookup); err == nil {
		t.Fatal("a single patch must not bypass a plan that changes multiple repositories")
	}
}

func TestRunImplementationReportsPartialRepositoryFailure(t *testing.T) {
	setupRepository := func(root string) {
		for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
			result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("git setup failed: %#v, %v", result, err)
			}
		}
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if result, err := runLocal(context.Background(), root, commandTimeout, "", "git", "add", "README.md"); err != nil || result.ExitCode != 0 {
			t.Fatalf("git add failed: %#v, %v", result, err)
		}
		if result, err := runLocal(context.Background(), root, commandTimeout, "", "git", "commit", "-m", "initial"); err != nil || result.ExitCode != 0 {
			t.Fatalf("git commit failed: %#v, %v", result, err)
		}
	}
	apiRoot, webRoot := filepath.Join(t.TempDir(), "api"), filepath.Join(t.TempDir(), "web")
	for _, root := range []string{apiRoot, webRoot} {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		setupRepository(root)
	}
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"api":{"path":"` + filepath.ToSlash(apiRoot) + `"},"web":{"path":"` + filepath.ToSlash(webRoot) + `"}}`
		}
		return ""
	}
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://api"},{"kind":"repository","reference":"workspace://web"}],"repository_topology":[{"reference":"workspace://api"},{"reference":"workspace://web","depends_on":["workspace://api"]}],"approved_plan":{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://web","impact":"changes"}]}}`)
	proposal := `{"summary":"partial two-repo delivery","patches":[{"repository_ref":"workspace://api","patch":"diff --git a/api.txt b/api.txt\nnew file mode 100644\n--- /dev/null\n+++ b/api.txt\n@@ -0,0 +1 @@\n+api\n"},{"repository_ref":"workspace://web","patch":"diff --git a/missing.txt b/missing.txt\n--- a/missing.txt\n+++ b/missing.txt\n@@ -1 +1 @@\n-old\n+new\n"}]}`
	result, err := RunImplementation(context.Background(), "a5a4b837-2e18-43af-9f58-6d59629db2bb", delivery, proposal, lookup)
	if err == nil {
		t.Fatal("a failed dependent repository must keep the implementation failed")
	}
	if partial, ok := result["partial"].(bool); !ok || !partial {
		t.Fatalf("expected partial reconciliation marker: %#v", result)
	}
	if result["failed_repository"] != "workspace://web" {
		t.Fatalf("failed repository = %#v, want workspace://web", result["failed_repository"])
	}
	completed, ok := result["completed_repositories"].([]string)
	if !ok || !reflect.DeepEqual(completed, []string{"workspace://api"}) {
		t.Fatalf("completed repositories = %#v", result["completed_repositories"])
	}
	if _, err := os.Stat(filepath.Join(apiRoot, ".itbem-agent-worktrees", "a5a4b837-2e18-43af-9f58-6d59629db2bb", "api.txt")); err != nil {
		t.Fatalf("completed repository evidence was not retained: %v", err)
	}
}

func setupImplementationRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v, %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git commit setup failed: %#v, %v", result, err)
		}
	}
	return root
}

func TestRunImplementationPreservesPartialMultirepositoryEvidenceOnFailure(t *testing.T) {
	apiRoot, webRoot := setupImplementationRepository(t), setupImplementationRepository(t)
	workspaceJSON := `{"api":{"path":"` + filepath.ToSlash(apiRoot) + `"},"web":{"path":"` + filepath.ToSlash(webRoot) + `"}}`
	lookup := func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return workspaceJSON
		}
		return ""
	}
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://api"},{"kind":"repository","reference":"workspace://web"}],"repository_topology":[{"reference":"workspace://api"},{"reference":"workspace://web","depends_on":["workspace://api"]}],"approved_plan":{"repository_impact":[{"reference":"workspace://api","impact":"changes"},{"reference":"workspace://web","impact":"changes"}]}}`)
	proposal := `{"summary":"partial multi-repo change","patches":[{"repository_ref":"workspace://api","patch":"diff --git a/api.txt b/api.txt\nnew file mode 100644\n--- /dev/null\n+++ b/api.txt\n@@ -0,0 +1 @@\n+api\n"},{"repository_ref":"workspace://web","patch":"diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1,1 +1,1 @@\n-NOT-THERE\n+web\n"}]}`
	result, err := RunImplementation(context.Background(), "a5a4b837-2e18-43af-9f58-6d59629db2bb", delivery, proposal, lookup)
	if err == nil {
		t.Fatal("a later repository failure must keep the implementation failed")
	}
	if result == nil || result["partial"] != true || result["failed_repository"] != "workspace://web" {
		t.Fatalf("partial reconciliation projection = %#v, error=%v", result, err)
	}
	completed, ok := result["completed_repositories"].([]string)
	if !ok || !reflect.DeepEqual(completed, []string{"workspace://api"}) {
		t.Fatalf("completed repository order = %#v", result["completed_repositories"])
	}
	changeSets, ok := result["change_sets"].([]any)
	if !ok || len(changeSets) != 1 {
		t.Fatalf("expected one completed private change set, got %#v", result["change_sets"])
	}
	if _, readErr := os.Stat(filepath.Join(apiRoot, ".itbem-agent-worktrees", "a5a4b837-2e18-43af-9f58-6d59629db2bb", "api.txt")); readErr != nil {
		t.Fatalf("completed repository evidence was not preserved: %v", readErr)
	}
}

func TestIsolatedWorktreeCopiesPinnedContractFixture(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v, %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := runLocal(context.Background(), root, commandTimeout, "", "git", "add", "README.md")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("git add failed: %#v, %v", result, err)
	}
	result, err = runLocal(context.Background(), root, commandTimeout, "", "git", "commit", "-m", "initial")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("git commit failed: %#v, %v", result, err)
	}
	contract := filepath.Join(root, ".contracts", "itbem-product-contract", "contract", "products.v1.json")
	if err := os.MkdirAll(filepath.Dir(contract), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contract, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(root, ".contracts", "itbem-product-contract", ".github", "workflows", "secret-scan.yml")
	if err := os.MkdirAll(filepath.Dir(workflow), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflow, []byte("name: Secret scan\n"), 0600); err != nil {
		t.Fatal(err)
	}
	worktree, _, err := isolatedWorktree(context.Background(), Workspace{Root: root, Config: WorkspaceConfig{ReadOnlyFixturePaths: []string{".contracts/itbem-product-contract"}}}, "a4a4b837-2e18-43af-9f58-6d59629db2bb")
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(worktree, ".contracts", "itbem-product-contract", "contract", "products.v1.json"))
	if err != nil || string(copied) != `{"version":1}` {
		t.Fatalf("pinned fixture was not copied safely: %q, %v", copied, err)
	}
	copiedWorkflow, err := os.ReadFile(filepath.Join(worktree, ".contracts", "itbem-product-contract", ".github", "workflows", "secret-scan.yml"))
	if err != nil || string(copiedWorkflow) != "name: Secret scan\n" {
		t.Fatalf("security workflow descriptor was not copied safely: %q, %v", copiedWorkflow, err)
	}
}

func TestPublicationAuthorizationRejectsExpiredAndUngovernedScopes(t *testing.T) {
	valid := &publicationAuthorization{GrantID: "d4a4b837-2e18-43af-9f58-6d59629db2bb", RepositoryRef: "workspace://repo", BaseSHA: strings.Repeat("a", 40), GitHubRepository: "itbem-corp/itbem-events-backend", ReviewDiffSHA256: strings.Repeat("b", 64), Branch: "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb", Capabilities: []string{"commit:stage", "branch:publish"}, ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
	if err := validatePublicationAuthorization(valid); err != nil {
		t.Fatalf("valid authorization rejected: %v", err)
	}
	expired := *valid
	expired.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if err := validatePublicationAuthorization(&expired); err == nil {
		t.Fatal("expired authorization must fail closed")
	}
	unsafe := *valid
	unsafe.Capabilities = append(unsafe.Capabilities, "deploy:production")
	if err := validatePublicationAuthorization(&unsafe); err == nil {
		t.Fatal("unsupported publication capability must fail closed")
	}
}

func TestParseGitHubRemoteRejectsCredentialsAndNonGitHubOrigins(t *testing.T) {
	valid, err := parseGitHubRemote("git@github.com:Itbem-Corp/itbem-events-backend.git")
	if err != nil || valid.Owner != "Itbem-Corp" || valid.Name != "itbem-events-backend" {
		t.Fatalf("expected safe GitHub origin: %#v, %v", valid, err)
	}
	if !publicationRepositoryMatches(valid, "itbem-corp/itbem-events-backend") || publicationRepositoryMatches(valid, "itbem-corp/another-repository") {
		t.Fatal("publication must be bound to the exact reviewed GitHub repository")
	}
	for _, remote := range []string{"https://token@github.com/Itbem-Corp/repo.git", "https://example.com/Itbem-Corp/repo.git", "git@github.com:Itbem-Corp/repo/extra.git"} {
		if _, err := parseGitHubRemote(remote); err == nil {
			t.Fatalf("unsafe origin accepted: %s", remote)
		}
	}
}

func TestPublicationRemoteBaseRequiresTheExactReviewedDefaultBranch(t *testing.T) {
	base := strings.Repeat("a", 40)
	auth := &publicationAuthorization{
		GrantID:          "d4a4b837-2e18-43af-9f58-6d59629db2bb",
		RepositoryRef:    "workspace://repo",
		BaseSHA:          base,
		GitHubRepository: "Itbem-Corp/test-repo",
		ReviewDiffSHA256: strings.Repeat("b", 64),
		Branch:           "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb",
		Capabilities:     []string{"commit:stage", "branch:publish"},
		ExpiresAt:        time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
	}
	remote := githubRepository{Owner: "Itbem-Corp", Name: "test-repo"}
	checkpoint := GitHubRepositorySnapshot{Reference: "github://Itbem-Corp/test-repo", Revision: base, DefaultBranch: "main"}
	if err := validatePublicationRemoteBase(checkpoint, remote, auth); err != nil {
		t.Fatalf("matching reviewed base should publish: %v", err)
	}
	checkpoint.Revision = strings.Repeat("c", 40)
	if err := validatePublicationRemoteBase(checkpoint, remote, auth); err == nil {
		t.Fatal("publication must reject a default branch that changed after review")
	}
	checkpoint.Revision = base
	checkpoint.Reference = "github://Itbem-Corp/another-repo"
	if err := validatePublicationRemoteBase(checkpoint, remote, auth); err == nil {
		t.Fatal("publication must reject a checkpoint for another repository")
	}
}
