package deliveryplansteps

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func TestNormalizeCreatesOrderedVersionedStepDAGForStandaloneWorkItem(t *testing.T) {
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: uuid.Must(uuid.NewV4()), Version: 4}
	inputs := []StepInput{
		{Key: "integrate", Role: StepRoleIntegration, Order: 30, Title: "Integrate", Objective: "Merge and verify", AcceptanceCriteria: []string{"Backend passes", "Frontend passes"}, DependsOn: []string{" ui ", "api"}},
		{Key: "ui", Role: StepRoleImplementation, Order: 20, Title: "Implement UI", Objective: "Make the UI change", AcceptanceCriteria: []string{"Frontend passes"}},
		{Key: "api", Role: StepRoleImplementation, Order: 10, Title: "Implement API", Objective: "Make the API change", AcceptanceCriteria: []string{"Backend passes"}},
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	normalized, err := Normalize(plan, "reviewer-1", inputs, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Steps) != 3 || len(normalized.Dependencies) != 2 {
		t.Fatalf("normalized graph = %d steps, %d edges", len(normalized.Steps), len(normalized.Dependencies))
	}
	if normalized.Steps[0].StepKey != "api" || normalized.Steps[1].StepKey != "ui" || normalized.Steps[2].StepKey != "integrate" || normalized.Steps[2].Role != StepRoleIntegration {
		t.Fatalf("steps must sort by explicit display order: %#v", normalized.Steps)
	}
	stepIDs := make(map[uuid.UUID]string)
	for _, step := range normalized.Steps {
		stepIDs[step.ID] = step.StepKey
		if step.PlanID != plan.ID || step.Status != models.DeliveryPlanStepPlanned || step.IdempotencyKey != "step:"+step.StepKey {
			t.Fatalf("step version/status/idempotency mismatch: %#v", step)
		}
	}
	for _, edge := range normalized.Dependencies {
		if edge.PlanID != plan.ID || edge.StepID == edge.DependsOnStepID || stepIDs[edge.StepID] == "" || stepIDs[edge.DependsOnStepID] == "" {
			t.Fatalf("invalid same-plan dependency: %#v", edge)
		}
	}
	if inputs[0].DependsOn[0] != "ui" || inputs[0].DependsOn[1] != "api" {
		t.Fatalf("dependency keys should be canonicalized before edge creation: %#v", inputs[0].DependsOn)
	}
}

func TestNormalizeRejectsInvalidStepGraph(t *testing.T) {
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: uuid.Must(uuid.NewV4()), Version: 1}
	base := func() []StepInput {
		return []StepInput{
			{Key: "alpha", Role: StepRoleImplementation, Order: 0, Title: "Alpha", AcceptanceCriteria: []string{"Done"}},
			{Key: "beta", Role: StepRoleIntegration, Order: 1, Title: "Beta", AcceptanceCriteria: []string{"Done"}, DependsOn: []string{"alpha"}},
		}
	}
	for _, scenario := range []struct {
		name   string
		mutate func([]StepInput) []StepInput
	}{
		{name: "missing dependency", mutate: func(input []StepInput) []StepInput { input[0].DependsOn = []string{"missing"}; return input }},
		{name: "self dependency", mutate: func(input []StepInput) []StepInput { input[0].DependsOn = []string{"alpha"}; return input }},
		{name: "cycle", mutate: func(input []StepInput) []StepInput { input[0].DependsOn = []string{"beta"}; return input }},
		{name: "repeated edge after canonicalization", mutate: func(input []StepInput) []StepInput { input[1].DependsOn = []string{"alpha", " alpha "}; return input }},
		{name: "duplicate order", mutate: func(input []StepInput) []StepInput { input[1].Order = input[0].Order; return input }},
		{name: "duplicate idempotency", mutate: func(input []StepInput) []StepInput {
			input[0].IdempotencyKey = "same"
			input[1].IdempotencyKey = "same"
			return input
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			input := scenario.mutate(base())
			if _, err := Normalize(plan, "actor", input, time.Now()); err == nil {
				t.Fatal("invalid graph should be rejected")
			}
		})
	}
	encoded, err := json.Marshal(StepInput{Key: "safe", Order: 0, Title: "Safe", AcceptanceCriteria: []string{"Done"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "reasoning", "result_blob", "provider_response"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("step input unexpectedly carries private field %q", forbidden)
		}
	}
}

func TestIdempotentReplayComparesNormalizedContentAndEdges(t *testing.T) {
	plan := models.DeliveryPlan{ID: uuid.Must(uuid.NewV4()), WorkItemID: uuid.Must(uuid.NewV4()), Version: 2}
	inputs := []StepInput{
		{Key: "prepare", Role: StepRoleImplementation, Order: 0, Title: "Prepare", Objective: "Review", AcceptanceCriteria: []string{"Reviewed"}},
		{Key: "build", Role: StepRoleImplementation, Order: 1, Title: "Build", Objective: "Implement", AcceptanceCriteria: []string{"Works"}, DependsOn: []string{"prepare"}},
		{Key: "integrate", Role: StepRoleIntegration, Order: 2, Title: "Integrate", Objective: "Merge and verify", AcceptanceCriteria: []string{"Reviewed", "Works"}, DependsOn: []string{"build"}},
	}
	requested, err := Normalize(plan, "actor", inputs, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	existing := append([]models.DeliveryPlanStep(nil), requested.Steps...)
	oldIDs := make(map[string]uuid.UUID, len(existing))
	for index := range existing {
		oldIDs[existing[index].StepKey] = uuid.Must(uuid.NewV4())
		existing[index].ID = oldIDs[existing[index].StepKey]
	}
	existingEdges := []models.DeliveryPlanStepDependency{
		{PlanID: plan.ID, StepID: oldIDs["build"], DependsOnStepID: oldIDs["prepare"]},
		{PlanID: plan.ID, StepID: oldIDs["integrate"], DependsOnStepID: oldIDs["build"]},
	}
	if !samePlanContent(requested, existing, existingEdges) {
		t.Fatal("exact retry should be idempotent despite database-generated row IDs")
	}
	existing[1].Objective = "Different plan content"
	if samePlanContent(requested, existing, existingEdges) {
		t.Fatal("changed content must not overwrite a versioned plan snapshot")
	}
}

func TestStepDTOOmitsMachineRunAndPrivateReferences(t *testing.T) {
	planID := uuid.Must(uuid.NewV4())
	stepID := uuid.Must(uuid.NewV4())
	workerID := "a69b7f51-58b9-4f0e-aef3-1fbc23f79826"
	machineID := "b69b7f51-58b9-4f0e-aef3-1fbc23f79826"
	taskID := uuid.Must(uuid.NewV4())
	executionID := uuid.Must(uuid.NewV4())
	steps := []models.DeliveryPlanStep{{
		ID: stepID, PlanID: planID, StepKey: "implement", Role: StepRoleImplementation, DisplayOrder: 1, Title: "Implement", Objective: "Bounded change",
		AcceptanceCriteriaJSON: `["Tests pass"]`, Status: models.DeliveryPlanStepRunning, AgentKey: "generalist",
		WorkerID: workerID, MachineID: machineID, RunID: "private-run-lease", AutomationTaskID: &taskID, AutomationExecutionID: &executionID,
	}}
	dtos, err := DTOs(steps, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(dtos)
	if err != nil {
		t.Fatal(err)
	}
	if len(dtos) != 1 || dtos[0].PlanVersion != 3 || dtos[0].AgentKey != "generalist" || dtos[0].AutomationTaskID != taskID.String() {
		t.Fatalf("unexpected safe step DTO: %#v", dtos)
	}
	for _, forbidden := range []string{workerID, machineID, "private-run-lease", "s3://", "prompt", "reasoning"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("step DTO exposed private/internal value %q: %s", forbidden, encoded)
		}
	}
}

func TestStepStateTransitionsKeepTerminalVersionsImmutable(t *testing.T) {
	for _, scenario := range []struct {
		from, to string
		allowed  bool
	}{
		{models.DeliveryPlanStepPlanned, models.DeliveryPlanStepReady, true},
		{models.DeliveryPlanStepReady, models.DeliveryPlanStepRunning, true},
		{models.DeliveryPlanStepRunning, models.DeliveryPlanStepCompleted, true},
		{models.DeliveryPlanStepFailed, models.DeliveryPlanStepReady, true},
		{models.DeliveryPlanStepCompleted, models.DeliveryPlanStepReady, false},
		{models.DeliveryPlanStepSkipped, models.DeliveryPlanStepRunning, false},
	} {
		if got := CanTransitionStatus(scenario.from, scenario.to); got != scenario.allowed {
			t.Errorf("CanTransitionStatus(%q, %q) = %v, want %v", scenario.from, scenario.to, got, scenario.allowed)
		}
	}
}
