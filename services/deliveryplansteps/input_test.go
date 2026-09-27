package deliveryplansteps

import "testing"

func TestInputsFromStructuredPlanSupportsParallelExecutionGraph(t *testing.T) {
	inputs, err := InputsFromStructuredPlan(map[string]any{
		"implementation_steps": []any{"Prepare", "Build API", "Build UI", "Integrate"},
		"acceptance_criteria":  []any{"Context is fixed", "Endpoint passes tests", "Screen renders"},
		"execution_steps": []any{
			map[string]any{"step_key": "prepare", "role": "implementation", "order": float64(1), "title": "Prepare", "objective": "Freeze context", "acceptance_criteria": []any{"Context is fixed"}, "depends_on": []any{}},
			map[string]any{"step_key": "api", "role": "implementation", "order": float64(2), "title": "Build API", "objective": "Implement the backend", "acceptance_criteria": []any{"Endpoint passes tests"}, "depends_on": []any{"prepare"}},
			map[string]any{"step_key": "ui", "role": "implementation", "order": float64(3), "title": "Build UI", "objective": "Implement the frontend", "acceptance_criteria": []any{"Screen renders"}, "depends_on": []any{"prepare"}},
			map[string]any{"step_key": "integrate", "role": "integration", "order": float64(4), "title": "Integrate", "objective": "Merge and verify the full change", "acceptance_criteria": []any{"Context is fixed", "Endpoint passes tests", "Screen renders"}, "depends_on": []any{"api", "ui"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 4 || len(inputs[1].DependsOn) != 1 || len(inputs[2].DependsOn) != 1 || inputs[1].DependsOn[0] != "prepare" || inputs[2].DependsOn[0] != "prepare" || inputs[3].Role != StepRoleIntegration || len(inputs[3].DependsOn) != 2 {
		t.Fatalf("parallel graph was not preserved: %#v", inputs)
	}
}

func TestInputsFromStructuredPlanRejectsLegacyStepsWithoutIntegration(t *testing.T) {
	_, err := InputsFromStructuredPlan(map[string]any{
		"implementation_steps": []any{"Review contract", "Implement endpoint"},
		"acceptance_criteria":  []any{"Routes are authorized", "Tests pass"},
	})
	if err == nil {
		t.Fatal("legacy executable steps without a frozen integration node must fail closed")
	}
}

func TestInputsFromStructuredPlanRejectsBrokenDependencyAndAllowsNoWork(t *testing.T) {
	if _, err := InputsFromStructuredPlan(map[string]any{
		"implementation_steps": []any{"Build", "Integrate"},
		"acceptance_criteria":  []any{"Done"},
		"execution_steps": []any{
			map[string]any{"step_key": "build", "role": "implementation", "order": float64(0), "title": "Build", "objective": "Build", "acceptance_criteria": []any{"Done"}, "depends_on": []any{"missing"}},
			map[string]any{"step_key": "integrate", "role": "integration", "order": float64(1), "title": "Integrate", "objective": "Merge", "acceptance_criteria": []any{"Done"}, "depends_on": []any{"build"}},
		},
	}); err == nil {
		t.Fatal("unknown step dependency must be rejected")
	}
	inputs, err := InputsFromStructuredPlan(map[string]any{"implementation_steps": []any{}, "acceptance_criteria": []any{}})
	if err != nil || len(inputs) != 0 {
		t.Fatalf("plan with no implementation work should not create synthetic steps: %#v, %v", inputs, err)
	}
}
