// Package agentwork defines the control-plane contract that assigns each
// supported operation to exactly one agent role and queue lane.
//
// An assignment is routing metadata only. It never grants repository,
// publication, merge, deployment, secret, or production authority. Those
// capabilities remain subject to the Project Vault, human approvals and the
// deterministic delivery gatekeeper.
package agentwork

type Role string

const (
	RoleOrchestrator      Role = "orchestrator"
	RolePrincipalEngineer Role = "principal_engineer"
	RoleReviewer          Role = "reviewer"
	RoleQA                Role = "qa"
	RoleReleaseManager    Role = "release_manager"
)

type Lane string

const (
	LaneOrchestration Lane = "orchestration"
	LaneEngineering   Lane = "engineering"
	LaneReview        Lane = "review"
	LaneQA            Lane = "qa"
	LaneRelease       Lane = "release"
)

const (
	OperationAIChat                 = "ai.chat"
	OperationDocumentAnalyze        = "document.analyze"
	OperationCodeReview             = "code.review"
	OperationProductIdeate          = "product.ideate"
	OperationDeliveryPlan           = "delivery.plan"
	OperationDeliveryImplementation = "delivery.implementation"
	OperationDeliveryPublish        = "delivery.publish"
	OperationDeliveryReleaseGate    = "delivery.release_gate"
	OperationDeliveryQA             = "delivery.qa"
	OperationDeliverySummary        = "delivery.summary"
)

type Assignment struct {
	Role Role
	Lane Lane
}

// AssignmentForOperation is deliberately exhaustive and fail-closed. Keeping
// this as a switch avoids exposing mutable routing state to callers.
func AssignmentForOperation(operation string) (Assignment, bool) {
	switch operation {
	case OperationAIChat, OperationDocumentAnalyze, OperationDeliverySummary:
		return Assignment{Role: RoleOrchestrator, Lane: LaneOrchestration}, true
	case OperationProductIdeate, OperationDeliveryPlan, OperationDeliveryImplementation:
		return Assignment{Role: RolePrincipalEngineer, Lane: LaneEngineering}, true
	case OperationCodeReview:
		return Assignment{Role: RoleReviewer, Lane: LaneReview}, true
	case OperationDeliveryQA:
		return Assignment{Role: RoleQA, Lane: LaneQA}, true
	case OperationDeliveryPublish, OperationDeliveryReleaseGate:
		return Assignment{Role: RoleReleaseManager, Lane: LaneRelease}, true
	default:
		return Assignment{}, false
	}
}

func IsSupportedOperation(operation string) bool {
	_, ok := AssignmentForOperation(operation)
	return ok
}

// IsKnownRoleLane accepts only the five deployable worker identities. Empty
// values are a migration concern handled by runtime configuration, not a sixth
// privileged identity.
func IsKnownRoleLane(role Role, lane Lane) bool {
	switch {
	case role == RoleOrchestrator && lane == LaneOrchestration:
		return true
	case role == RolePrincipalEngineer && lane == LaneEngineering:
		return true
	case role == RoleReviewer && lane == LaneReview:
		return true
	case role == RoleQA && lane == LaneQA:
		return true
	case role == RoleReleaseManager && lane == LaneRelease:
		return true
	default:
		return false
	}
}
