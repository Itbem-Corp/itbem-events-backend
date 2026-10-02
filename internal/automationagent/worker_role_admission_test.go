package automationagent

import (
	"context"
	"errors"
	"events-stocks/internal/agentwork"
	"testing"
)

func TestWorkerRoleAdmissionRejectsEveryCrossLaneOperationBeforeClaim(t *testing.T) {
	operations := []string{"ai.chat", "delivery.chat", "document.analyze", "delivery.summary", "product.ideate", "delivery.plan", "delivery.implementation", "code.review", "delivery.assessment", "delivery.qa", "delivery.onboarding_probe", "delivery.publish"}
	roles := []agentwork.Assignment{
		{Role: agentwork.RoleOrchestrator, Lane: agentwork.LaneOrchestration},
		{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering},
		{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview},
		{Role: agentwork.RoleQA, Lane: agentwork.LaneQA},
		{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease},
	}
	for _, role := range roles {
		for _, operation := range operations {
			t.Run(string(role.Lane)+"/"+operation, func(t *testing.T) {
				assigned, known := agentwork.AssignmentForOperation(operation)
				if !known {
					t.Fatalf("unknown test operation %s", operation)
				}
				for _, allowed := range [][]string{nil, operations} {
					callback := &fakeCallback{}
					worker, err := NewWorker(WorkerConfig{InputBucket: "fixture-input", OutputBucket: "fixture-output", Role: role.Role, Lane: role.Lane, AllowedOperations: allowed}, &fakeStore{}, callback, fakeProvider{err: errors.New("provider must not run for cross-role work")})
					if err != nil {
						t.Fatal(err)
					}
					want := assigned == role
					if worker.canProcess(operation) != want {
						t.Fatalf("admission for %s does not enforce its assigned lane", operation)
					}
					if !want {
						message := validMessage()
						message.Payload.Operation = operation
						message.Payload.InputRef = "s3://fixture-input/automation/inputs/task/input.json"
						var retryable *RetryableError
						if err := worker.Process(context.Background(), message); !errors.As(err, &retryable) {
							t.Fatalf("wrong-role work must be retained for its owner: %v", err)
						}
						if len(callback.updates) != 0 {
							t.Fatal("wrong-role work reached the claim callback")
						}
					}
				}
			})
		}
	}
}

func TestWorkerRoleAdmissionRetainsSpecialistIntersectionAndLegacyCompatibility(t *testing.T) {
	for _, test := range []struct {
		role      agentwork.Role
		lane      agentwork.Lane
		allowed   []string
		operation string
		want      bool
	}{
		{agentwork.RolePrincipalEngineer, agentwork.LaneEngineering, []string{"delivery.plan"}, "delivery.plan", true},
		{agentwork.RolePrincipalEngineer, agentwork.LaneEngineering, []string{"delivery.plan"}, "delivery.implementation", false},
		{agentwork.RoleReviewer, agentwork.LaneReview, nil, "unknown.operation", false},
		{"", "", nil, "delivery.plan", true},
		{"", "", []string{"code.review"}, "delivery.plan", false},
	} {
		worker, err := NewWorker(WorkerConfig{InputBucket: "fixture-input", OutputBucket: "fixture-output", Role: test.role, Lane: test.lane, AllowedOperations: test.allowed}, &fakeStore{}, &fakeCallback{}, fakeProvider{})
		if err != nil {
			t.Fatal(err)
		}
		if got := worker.canProcess(test.operation); got != test.want {
			t.Fatalf("role=%s operation=%s admission=%v want=%v", test.role, test.operation, got, test.want)
		}
	}
}
