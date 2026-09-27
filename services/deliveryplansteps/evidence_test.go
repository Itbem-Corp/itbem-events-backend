package deliveryplansteps

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func TestNormalizeAndDTOProjectBoundedStepEvidenceRequirements(t *testing.T) {
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: uuid.Must(uuid.NewV4()), Version: 3}
	inputs := []StepInput{
		{
			Key: "qa", Role: StepRoleImplementation, Order: 1, Title: "Validate", Objective: "Run checks",
			AcceptanceCriteria: []string{"Checks pass"},
			EvidenceRequirements: []EvidenceRequirement{{
				Key: "test-report", Title: "Test report", Description: "Attach sanitized test output", Required: true,
				ContentTypes: []string{"text/plain", "application/json"}, MaxBytes: 1 << 20,
			}},
		},
		{Key: "integrate", Role: StepRoleIntegration, Order: 2, Title: "Integrate", Objective: "Verify checks", AcceptanceCriteria: []string{"Checks pass"}, DependsOn: []string{"qa"}},
	}
	normalized, err := Normalize(plan, "reviewer", inputs, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Steps[0].EvidenceRequirementsJSON != `[{"key":"test-report","title":"Test report","description":"Attach sanitized test output","required":true,"content_types":["application/json","text/plain"],"max_bytes":1048576}]` {
		t.Fatalf("evidence requirement was not normalized canonically: %s", normalized.Steps[0].EvidenceRequirementsJSON)
	}
	items, err := DTOs(normalized.Steps, nil, plan.Version)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["evidence_requirements"]; !ok || strings.Contains(string(encoded), "bucket") || strings.Contains(string(encoded), "object_key") {
		t.Fatalf("safe step DTO omitted requirements or exposed private storage: %s", encoded)
	}
}

func TestEvidenceRequirementBoundsAndMIMETypesAreEnforced(t *testing.T) {
	base := EvidenceRequirement{Key: "report", Title: "Report", Required: true, ContentTypes: []string{"text/plain"}, MaxBytes: 128}
	for _, test := range []struct {
		name string
		edit func(*EvidenceRequirement)
	}{
		{name: "unsupported MIME", edit: func(r *EvidenceRequirement) { r.ContentTypes = []string{"text/html"} }},
		{name: "oversized payload", edit: func(r *EvidenceRequirement) { r.MaxBytes = MaxStepEvidenceBytes + 1 }},
		{name: "missing content type", edit: func(r *EvidenceRequirement) { r.ContentTypes = nil }},
		{name: "invalid key", edit: func(r *EvidenceRequirement) { r.Key = "../report" }},
		{name: "blank title", edit: func(r *EvidenceRequirement) { r.Title = " " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			requirement := base
			test.edit(&requirement)
			if _, err := normalizeEvidenceRequirements([]EvidenceRequirement{requirement}); err == nil {
				t.Fatal("invalid evidence requirement was accepted")
			}
		})
	}
	duplicate := base
	if _, err := normalizeEvidenceRequirements([]EvidenceRequirement{base, duplicate}); err == nil {
		t.Fatal("duplicate evidence requirement keys were accepted")
	}
	if _, err := normalizeEvidenceRequirements(make([]EvidenceRequirement, MaxEvidenceRequirements+1)); err == nil {
		t.Fatal("too many requirements were accepted")
	}
}

func TestInputsFromStructuredPlanParsesEvidenceRequirementsStrictly(t *testing.T) {
	requirement := map[string]any{
		"key": "qa-report", "title": "QA report", "required": true,
		"content_types": []any{"application/json"}, "max_bytes": float64(4096),
	}
	structured := map[string]any{
		"acceptance_criteria": []any{"Test suite passes"},
		"execution_steps": []any{
			map[string]any{"step_key": "implement", "role": "implementation", "order": float64(1), "title": "Implement", "objective": "Build feature", "acceptance_criteria": []any{"Test suite passes"}, "evidence_requirements": []any{requirement}},
			map[string]any{"step_key": "integrate", "role": "integration", "order": float64(2), "title": "Integrate", "objective": "Verify feature", "acceptance_criteria": []any{"Test suite passes"}, "depends_on": []any{"implement"}},
		},
	}
	inputs, err := InputsFromStructuredPlan(structured)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 || len(inputs[0].EvidenceRequirements) != 1 || inputs[0].EvidenceRequirements[0].Key != "qa-report" {
		t.Fatalf("structured requirements were not preserved: %#v", inputs)
	}
	requirement["unexpected"] = "rejected"
	if _, err := InputsFromStructuredPlan(structured); err == nil {
		t.Fatal("unknown evidence requirement fields must be rejected")
	}
}
