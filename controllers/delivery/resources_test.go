package delivery

import (
	"testing"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestDeliveryPlanResultKeyAcceptsOnlyExactTaskRun(t *testing.T) {
	taskID, runID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	cfg := &models.Config{AutomationOutputBucket: "private-results"}
	task := models.AutomationTask{ID: taskID, RunID: runID.String(), OutputRef: "s3://private-results/automation/" + taskID.String() + "/runs/" + runID.String() + "/result.json"}
	key, ok := deliveryPlanResultKey(cfg, task)
	if !ok || key != "automation/"+taskID.String()+"/runs/"+runID.String()+"/result.json" {
		t.Fatalf("exact immutable run result rejected: %q / %v", key, ok)
	}
	legacy := task
	legacy.OutputRef = "s3://private-results/automation/" + taskID.String() + "/result.json"
	legacy.RunID = ""
	if _, ok := deliveryPlanResultKey(cfg, legacy); !ok {
		t.Fatal("legacy task-scoped compatibility pointer without a run id was rejected")
	}
	for _, invalid := range []string{
		"s3://another-bucket/automation/" + taskID.String() + "/runs/" + runID.String() + "/result.json",
		"s3://private-results/automation/" + taskID.String() + "/runs/" + uuid.Must(uuid.NewV4()).String() + "/result.json",
		"s3://private-results/automation/" + uuid.Must(uuid.NewV4()).String() + "/runs/" + runID.String() + "/result.json",
	} {
		task.OutputRef = invalid
		if _, ok := deliveryPlanResultKey(cfg, task); ok {
			t.Fatalf("unbound plan result was accepted: %s", invalid)
		}
	}
}

func TestChangeSetMetadataCannotClaimAgentOrGitHubAppProvenance(t *testing.T) {
	if containsReservedChangeSetProvenance(map[string]any{"external_ticket": "OPS-42"}) {
		t.Fatal("ordinary human evidence metadata must remain available")
	}
	for _, metadata := range []map[string]any{
		{"verification_source": "itbem-github-app"},
		{"branch_published": true},
		{"automation_task_id": "task-id"},
		{"review_diff_sha256": "digest"},
	} {
		if !containsReservedChangeSetProvenance(metadata) {
			t.Fatalf("reserved provenance was accepted: %#v", metadata)
		}
	}
}

func TestValidatePlanStructureRequiresReviewableFields(t *testing.T) {
	valid := map[string]any{
		"goal_interpretation":  "Deliver a bounded reviewable change",
		"confidence":           0.85,
		"autonomy_boundary":    "Wait for human approval before implementation and release.",
		"context_reviewed":     []any{"workspace://backend@revision"},
		"context_gaps":         []any{},
		"assumptions":          []any{},
		"human_decisions":      []any{},
		"implementation_steps": []any{"Edit the bounded controller"},
		"risks":                []any{},
		"qa_plan":              []any{"Run delivery tests"},
		"evidence_plan":        []any{"QA report"},
		"acceptance_criteria":  []any{"Human gate remains mandatory"},
		"repository_impact": []any{map[string]any{
			"name": "Backend", "reference": "workspace://backend", "revision": "0123456789abcdef0123456789abcdef01234567", "role": "primary", "impact": "consulted", "notes": "Planning only",
		}},
		"files_impacted": []any{"controllers/delivery/resources.go"},
		"rollback_plan":  []any{"Revert the bounded change"},
		"questions":      []any{},
		"estimate":       "30 minutes",
	}
	if err := validatePlanStructure(valid); err != nil {
		t.Fatalf("valid structured plan rejected: %v", err)
	}
	delete(valid, "files_impacted")
	if err := validatePlanStructure(valid); err == nil {
		t.Fatal("expected plan without impacted files to be rejected")
	}
	valid["files_impacted"] = []any{"controllers/delivery/resources.go"}
	valid["repository_impact"] = []any{map[string]any{"name": "Backend", "reference": "workspace://backend", "revision": "rev", "role": "primary", "impact": "invalid", "notes": "Planning only"}}
	if err := validatePlanStructure(valid); err == nil {
		t.Fatal("expected plan with invalid repository impact to be rejected")
	}
}

func TestValidatePlanStructureRejectsUnsafeBrowserQACasesBeforeApproval(t *testing.T) {
	plan := map[string]any{
		"goal_interpretation": "Validate the approved preview", "confidence": 0.8, "autonomy_boundary": "Wait for human gates.",
		"context_reviewed": []any{}, "context_gaps": []any{}, "assumptions": []any{}, "human_decisions": []any{}, "implementation_steps": []any{"Implement"}, "risks": []any{}, "qa_plan": []any{"Run browser QA"}, "evidence_plan": []any{"Screenshot"}, "acceptance_criteria": []any{"Passed"}, "files_impacted": []any{}, "rollback_plan": []any{"Revert"}, "questions": []any{}, "estimate": "30m",
		"repository_impact": []any{},
		"browser_qa_mode":   "read_only",
		"browser_qa_cases":  []any{map[string]any{"id": "preview", "title": "Preview", "steps": []any{map[string]any{"kind": "click", "selector": "button", "expected_path": "/next"}}}},
	}
	if err := validatePlanStructure(plan); err == nil {
		t.Fatal("unsafe browser interaction must be rejected before a human can approve the plan")
	}
}

func TestValidatePlanRepositoryTopologyMakesContextBindingMandatory(t *testing.T) {
	structured := map[string]any{"repository_impact": []any{map[string]any{
		"name": "Backend", "reference": "workspace://backend", "revision": "abc123", "role": "primary", "impact": "consulted", "notes": "Planning only",
	}}}
	topology := []deliveryAgentRepository{{Name: "Backend", Reference: "workspace://backend", Revision: "abc123", Role: "primary"}}
	if err := validatePlanRepositoryTopology(structured, topology); err != nil {
		t.Fatalf("expected exact repository matrix to be accepted: %v", err)
	}
	structured["repository_impact"].([]any)[0].(map[string]any)["revision"] = "other"
	if err := validatePlanRepositoryTopology(structured, topology); err == nil {
		t.Fatal("expected mismatched revision to be rejected")
	}
	structured["repository_impact"] = []any{}
	if err := validatePlanRepositoryTopology(structured, topology); err == nil {
		t.Fatal("expected omitted frozen repository to be rejected")
	}
}

func TestValidatePlanRepositoryTopologyRejectsManualChangesToGitHubOnlyContext(t *testing.T) {
	structured := map[string]any{"repository_impact": []any{
		map[string]any{"name": "Backend", "reference": "workspace://backend", "revision": "abc123", "role": "primary", "impact": "changes", "notes": "Local implementation"},
		map[string]any{"name": "Dashboard", "reference": "github://Itbem-Corp/dashboard", "revision": "def456", "role": "supporting", "impact": "changes", "notes": "Remote only"},
	}}
	topology := []deliveryAgentRepository{
		{Name: "Backend", Reference: "workspace://backend", Revision: "abc123", Role: "primary"},
		{Name: "Dashboard", Reference: "github://Itbem-Corp/dashboard", Revision: "def456", Role: "supporting"},
	}
	if err := validatePlanRepositoryTopology(structured, topology); err == nil {
		t.Fatal("manual plan cannot bypass workspace-only implementation boundary")
	}
	structured["repository_impact"].([]any)[1].(map[string]any)["impact"] = "consulted"
	if err := validatePlanRepositoryTopology(structured, topology); err != nil {
		t.Fatalf("remote repository remains valid as a consulted checkpoint: %v", err)
	}
}

func TestValidateReleaseStructureRequiresReadableHumanDelivery(t *testing.T) {
	executive := map[string]any{
		"what_changed": "A clearer delivery report",
		"why":          "Reduce review friction",
		"how_to_test":  "Open the delivery work item",
		"risks":        []any{"No release without human approval"},
	}
	technical := map[string]any{
		"decisions": []any{"Keep human gates mandatory"},
		"evidence":  []any{"Local QA passed"},
	}
	if err := validateReleaseStructure(executive, technical); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}

	delete(executive, "how_to_test")
	if err := validateReleaseStructure(executive, technical); err == nil {
		t.Fatal("expected a release without human verification steps to be rejected")
	}

	executive["how_to_test"] = "Open the delivery work item"
	technical["evidence"] = []any{""}
	if err := validateReleaseStructure(executive, technical); err == nil {
		t.Fatal("expected blank evidence to be rejected")
	}
}

func TestDeliveryRequestTitleDerivesACompactLabelFromIntent(t *testing.T) {
	if got := deliveryRequestTitle("  Ajustar navegación  ", "Ignorado"); got != "Ajustar navegación" {
		t.Fatalf("explicit title changed: %q", got)
	}
	if got := deliveryRequestTitle("", "  Mejorar   la experiencia móvil del dashboard  "); got != "Mejorar la experiencia móvil del dashboard" {
		t.Fatalf("intent was not normalized: %q", got)
	}
	if got := deliveryRequestTitle("", ""); got != "" {
		t.Fatalf("empty intent should stay empty: %q", got)
	}
}

func TestNormalizeRepositoryContextMetadataRejectsMalformedTopologyEarly(t *testing.T) {
	metadata, err := normalizeRepositoryContextMetadata("workspace://backend", map[string]any{
		"repository_role":           "PRIMARY",
		"repository_kind":           "BACKEND_API",
		"repository_responsibility": " Delivery control plane ",
		"depends_on_repositories":   []any{"github://Itbem-Corp/dashboard-ts"},
	})
	if err != nil {
		t.Fatalf("valid repository topology rejected: %v", err)
	}
	if metadata["repository_role"] != "primary" || metadata["repository_kind"] != "backend_api" || metadata["repository_responsibility"] != "Delivery control plane" {
		t.Fatalf("repository metadata was not normalized: %#v", metadata)
	}
	dependencies, ok := metadata["depends_on_repositories"].([]string)
	if !ok || len(dependencies) != 1 || dependencies[0] != "github://Itbem-Corp/dashboard-ts" {
		t.Fatalf("repository dependencies were not normalized: %#v", metadata)
	}

	for _, candidate := range []map[string]any{
		{"repository_role": "owner"},
		{"repository_kind": "cron_script"},
		{"depends_on_repositories": []any{"workspace://backend"}},
		{"depends_on_repositories": []any{"workspace://frontend", "workspace://frontend"}},
	} {
		if _, err := normalizeRepositoryContextMetadata("workspace://backend", candidate); err == nil {
			t.Fatalf("expected malformed topology to be rejected: %#v", candidate)
		}
	}
	if _, err := normalizeRepositoryContextMetadata("https://github.com/Itbem-Corp/backend", map[string]any{}); err == nil {
		t.Fatal("expected non-registered repository reference to be rejected")
	}
}

func TestCanonicalDeliveryRepositoryReferenceNormalizesOnlyWorkspaceSyntax(t *testing.T) {
	if got := canonicalDeliveryRepositoryReference(" workspace://backend/ "); got != "workspace://backend" {
		t.Fatalf("workspace reference was not canonicalized: %q", got)
	}
	if got := canonicalDeliveryRepositoryReference("github://Itbem-Corp/repo"); got != "github://Itbem-Corp/repo" {
		t.Fatalf("GitHub reference changed unexpectedly: %q", got)
	}
}

func TestMergeDeliveryRepositoryMetadataChangesOnlyHumanOwnedArchitecture(t *testing.T) {
	existing := `{"workspace_capabilities":["repository:read"],"local_git_branch":"main","repository_role":"primary","repository_kind":"backend_api","repository_responsibility":"Old API","excerpt":"Preserve me"}`
	metadata, err := mergeDeliveryRepositoryMetadata("workspace://backend", existing, map[string]any{
		"repository_kind":           "worker",
		"repository_responsibility": "",
		"depends_on_repositories":   []any{"workspace://frontend"},
	})
	if err != nil {
		t.Fatalf("valid architecture update rejected: %v", err)
	}
	if metadata["repository_kind"] != "worker" || metadata["repository_responsibility"] != nil || metadata["excerpt"] != "Preserve me" {
		t.Fatalf("human architecture fields were not merged predictably: %#v", metadata)
	}
	if metadata["workspace_capabilities"] == nil || metadata["local_git_branch"] != "main" {
		t.Fatalf("control-plane checkpoint metadata must survive a topology update: %#v", metadata)
	}
	if _, err := mergeDeliveryRepositoryMetadata("workspace://backend", existing, map[string]any{"workspace_capabilities": []any{"repository:publish"}}); err == nil {
		t.Fatal("operator architecture update must not overwrite control-plane capabilities")
	}
}
