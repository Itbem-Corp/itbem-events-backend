package deliveryplansteps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func planHashFixture() (models.DeliveryPlan, []models.DeliveryPlanStep, []models.DeliveryPlanStepDependency) {
	planID := uuid.Must(uuid.NewV4())
	gateID := uuid.Must(uuid.NewV4())
	firstID, secondID, integrationID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	plan := models.DeliveryPlan{ID: planID, Version: 4, Status: "approved", ApprovedGateID: &gateID}
	steps := []models.DeliveryPlanStep{
		{ID: firstID, PlanID: planID, StepKey: "foundation", Role: StepRoleImplementation, DisplayOrder: 0, Title: "Foundation", Objective: "Establish shared contract", AcceptanceCriteriaJSON: `["tests pass", "no secrets"]`},
		{ID: secondID, PlanID: planID, StepKey: "feature", Role: StepRoleImplementation, DisplayOrder: 1, Title: "Feature", Objective: "Implement independent feature", AcceptanceCriteriaJSON: `["behavior verified"]`},
		{ID: integrationID, PlanID: planID, StepKey: "integrate", Role: StepRoleIntegration, DisplayOrder: 2, Title: "Integrate", Objective: "Merge and verify", AcceptanceCriteriaJSON: `["tests pass", "no secrets", "behavior verified"]`},
	}
	dependencies := []models.DeliveryPlanStepDependency{
		{PlanID: planID, StepID: integrationID, DependsOnStepID: firstID},
		{PlanID: planID, StepID: integrationID, DependsOnStepID: secondID},
	}
	return plan, steps, dependencies
}

func TestApprovedPlanContentHashIsStableAcrossRowAndJSONOrdering(t *testing.T) {
	plan, steps, dependencies := planHashFixture()
	want, err := ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	steps[0], steps[1] = steps[1], steps[0]
	steps[0].AcceptanceCriteriaJSON = `[ "behavior verified" ]`
	got, err := ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil || got != want {
		t.Fatalf("equivalent plan hash = %q, err=%v; want %q", got, err, want)
	}
	if len(got) != 64 || strings.ToLower(got) != got {
		t.Fatalf("plan hash must be lowercase SHA-256 hex, got %q", got)
	}
}

func TestApprovedPlanContentHashChangesForExecutablePlanMutations(t *testing.T) {
	plan, steps, dependencies := planHashFixture()
	want, err := ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name string
		edit func(*models.DeliveryPlan, []models.DeliveryPlanStep, []models.DeliveryPlanStepDependency)
	}{
		{name: "objective", edit: func(_ *models.DeliveryPlan, steps []models.DeliveryPlanStep, _ []models.DeliveryPlanStepDependency) {
			steps[0].Objective += " revised"
		}},
		{name: "acceptance criteria", edit: func(_ *models.DeliveryPlan, steps []models.DeliveryPlanStep, _ []models.DeliveryPlanStepDependency) {
			steps[0].AcceptanceCriteriaJSON = `["updated tests", "no secrets"]`
			steps[2].AcceptanceCriteriaJSON = `["updated tests", "no secrets", "behavior verified"]`
		}},
		{name: "step order", edit: func(_ *models.DeliveryPlan, steps []models.DeliveryPlanStep, _ []models.DeliveryPlanStepDependency) {
			steps[0].DisplayOrder = 1
		}},
		{name: "dependency", edit: func(_ *models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency) {
			dependencies[0].StepID = steps[1].ID
		}},
		{name: "role", edit: func(_ *models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency) {
			steps[0].Role, steps[2].Role = StepRoleIntegration, StepRoleImplementation
			steps[0].DisplayOrder, steps[2].DisplayOrder = 2, 0
			steps[0].AcceptanceCriteriaJSON = `["tests pass", "no secrets", "behavior verified"]`
			steps[2].AcceptanceCriteriaJSON = `["tests pass"]`
			steps[1].AcceptanceCriteriaJSON = `["no secrets", "behavior verified"]`
			dependencies[0] = models.DeliveryPlanStepDependency{PlanID: steps[0].PlanID, StepID: steps[0].ID, DependsOnStepID: steps[2].ID}
			dependencies[1] = models.DeliveryPlanStepDependency{PlanID: steps[0].PlanID, StepID: steps[0].ID, DependsOnStepID: steps[1].ID}
		}},
		{name: "approval gate", edit: func(plan *models.DeliveryPlan, _ []models.DeliveryPlanStep, _ []models.DeliveryPlanStepDependency) {
			next := uuid.Must(uuid.NewV4())
			plan.ApprovedGateID = &next
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changedPlan, changedSteps, changedDependencies := plan, append([]models.DeliveryPlanStep(nil), steps...), append([]models.DeliveryPlanStepDependency(nil), dependencies...)
			mutation.edit(&changedPlan, changedSteps, changedDependencies)
			got, err := ApprovedPlanContentHash(changedPlan, changedSteps, changedDependencies)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("mutated executable plan must produce a different digest")
			}
		})
	}
}

func TestApprovedPlanContentHashPreservesLegacyBytesForEmptyEvidenceRequirements(t *testing.T) {
	plan, steps, dependencies := planHashFixture()
	want := legacyApprovedPlanContentHash(t, plan, steps, dependencies)
	for _, raw := range []string{"", "[]", "null"} {
		for index := range steps {
			steps[index].EvidenceRequirementsJSON = raw
		}
		got, err := ApprovedPlanContentHash(plan, steps, dependencies)
		if err != nil || got != want {
			t.Fatalf("empty evidence requirements changed the legacy plan hash: got %q, want %q, err=%v", got, want, err)
		}
	}
	steps[0].EvidenceRequirementsJSON = `[{"key":"report","title":"Report","required":true,"content_types":["text/plain"],"max_bytes":1024}]`
	got, err := ApprovedPlanContentHash(plan, steps, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("non-empty evidence requirements must alter the approved-plan hash")
	}
}

func legacyApprovedPlanContentHash(t *testing.T, plan models.DeliveryPlan, steps []models.DeliveryPlanStep, dependencies []models.DeliveryPlanStepDependency) string {
	t.Helper()
	type legacyStep struct {
		ID                 string   `json:"id"`
		StepKey            string   `json:"step_key"`
		Role               string   `json:"role"`
		DisplayOrder       int      `json:"display_order"`
		Title              string   `json:"title"`
		Objective          string   `json:"objective"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
	}
	type dependency struct {
		StepID      string `json:"step_id"`
		DependsOnID string `json:"depends_on_step_id"`
	}
	type legacyPlan struct {
		SchemaVersion int          `json:"schema_version"`
		PlanID        string       `json:"plan_id"`
		Version       int          `json:"version"`
		ApprovedGate  string       `json:"approved_gate_id"`
		Steps         []legacyStep `json:"steps"`
		Dependencies  []dependency `json:"dependencies"`
	}
	orderedSteps := append([]models.DeliveryPlanStep(nil), steps...)
	sort.Slice(orderedSteps, func(i, j int) bool {
		if orderedSteps[i].DisplayOrder != orderedSteps[j].DisplayOrder {
			return orderedSteps[i].DisplayOrder < orderedSteps[j].DisplayOrder
		}
		return orderedSteps[i].ID.String() < orderedSteps[j].ID.String()
	})
	canonicalSteps := make([]legacyStep, 0, len(orderedSteps))
	for _, step := range orderedSteps {
		var criteria []string
		if err := json.Unmarshal([]byte(step.AcceptanceCriteriaJSON), &criteria); err != nil {
			t.Fatal(err)
		}
		canonicalSteps = append(canonicalSteps, legacyStep{ID: step.ID.String(), StepKey: step.StepKey, Role: step.Role, DisplayOrder: step.DisplayOrder, Title: step.Title, Objective: step.Objective, AcceptanceCriteria: criteria})
	}
	canonicalDependencies := make([]dependency, 0, len(dependencies))
	for _, edge := range dependencies {
		canonicalDependencies = append(canonicalDependencies, dependency{StepID: edge.StepID.String(), DependsOnID: edge.DependsOnStepID.String()})
	}
	sort.Slice(canonicalDependencies, func(i, j int) bool {
		if canonicalDependencies[i].StepID != canonicalDependencies[j].StepID {
			return canonicalDependencies[i].StepID < canonicalDependencies[j].StepID
		}
		return canonicalDependencies[i].DependsOnID < canonicalDependencies[j].DependsOnID
	})
	gateID := ""
	if plan.ApprovedGateID != nil {
		gateID = plan.ApprovedGateID.String()
	}
	encoded, err := json.Marshal(legacyPlan{SchemaVersion: 2, PlanID: plan.ID.String(), Version: plan.Version, ApprovedGate: gateID, Steps: canonicalSteps, Dependencies: canonicalDependencies})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func TestApprovedPlanContentHashRejectsInvalidApprovalOrGraph(t *testing.T) {
	plan, steps, dependencies := planHashFixture()
	plan.Status = "proposed"
	if _, err := ApprovedPlanContentHash(plan, steps, dependencies); err == nil {
		t.Fatal("unapproved plan must not be hashed as executable")
	}
	plan.Status = "approved"
	dependencies[0].DependsOnStepID = uuid.Must(uuid.NewV4())
	if _, err := ApprovedPlanContentHash(plan, steps, dependencies); err == nil {
		t.Fatal("dependency outside the plan must be rejected")
	}
}
