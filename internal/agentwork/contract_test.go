package agentwork

import "testing"

func TestAssignmentForOperationIsExhaustiveAndRoleIsolated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		operation string
		role      Role
		lane      Lane
	}{
		{OperationAIChat, RoleOrchestrator, LaneOrchestration},
		{OperationDocumentAnalyze, RoleOrchestrator, LaneOrchestration},
		{OperationDeliverySummary, RoleOrchestrator, LaneOrchestration},
		{OperationProductIdeate, RolePrincipalEngineer, LaneEngineering},
		{OperationDeliveryPlan, RolePrincipalEngineer, LaneEngineering},
		{OperationDeliveryImplementation, RolePrincipalEngineer, LaneEngineering},
		{OperationCodeReview, RoleReviewer, LaneReview},
		{OperationDeliveryQA, RoleQA, LaneQA},
		{OperationDeliveryOnboardingProbe, RoleQA, LaneQA},
		{OperationDeliveryPublish, RoleReleaseManager, LaneRelease},
		{OperationDeliveryReleaseGate, RoleReleaseManager, LaneRelease},
	}

	seen := make(map[string]struct{}, len(tests))
	for _, test := range tests {
		if _, duplicate := seen[test.operation]; duplicate {
			t.Fatalf("operation %q appears more than once", test.operation)
		}
		seen[test.operation] = struct{}{}
		t.Run(test.operation, func(t *testing.T) {
			assignment, ok := AssignmentForOperation(test.operation)
			if !ok {
				t.Fatalf("operation %q is not supported", test.operation)
			}
			if assignment.Role != test.role || assignment.Lane != test.lane {
				t.Fatalf("assignment for %q = %#v, want role=%q lane=%q", test.operation, assignment, test.role, test.lane)
			}
		})
	}
}

func TestAssignmentForOperationFailsClosed(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"", "code.merge", " code.review", "code.review ", "delivery.deploy", "DELIVERY.QA"} {
		if assignment, ok := AssignmentForOperation(operation); ok {
			t.Fatalf("unsupported operation %q received assignment %#v", operation, assignment)
		}
		if IsSupportedOperation(operation) {
			t.Fatalf("unsupported operation %q was allowlisted", operation)
		}
	}
}

func TestIsKnownRoleLaneRejectsCrossRoleAndInventedIdentities(t *testing.T) {
	t.Parallel()
	for _, assignment := range []Assignment{
		{Role: RoleOrchestrator, Lane: LaneOrchestration},
		{Role: RolePrincipalEngineer, Lane: LaneEngineering},
		{Role: RoleReviewer, Lane: LaneReview},
		{Role: RoleQA, Lane: LaneQA},
		{Role: RoleReleaseManager, Lane: LaneRelease},
	} {
		if !IsKnownRoleLane(assignment.Role, assignment.Lane) {
			t.Fatalf("known assignment rejected: %#v", assignment)
		}
	}
	for _, assignment := range []Assignment{
		{},
		{Role: RoleReviewer, Lane: LaneRelease},
		{Role: Role("admin"), Lane: Lane("production")},
		{Role: RolePrincipalEngineer, Lane: LaneReview},
	} {
		if IsKnownRoleLane(assignment.Role, assignment.Lane) {
			t.Fatalf("invalid assignment accepted: %#v", assignment)
		}
	}
}
