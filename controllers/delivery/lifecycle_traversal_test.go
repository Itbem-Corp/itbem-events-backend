package delivery

import (
	"testing"
	"time"

	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"github.com/gofrs/uuid"
)

// TestLocalDeliveryLifecycleTraversal exercises the complete durable state
// sequence used by the control plane. It deliberately stays local and pure:
// no provider, queue, GitHub publication or release side effect is involved.
// The controller still owns the stronger DB prerequisites (agent artifacts,
// previews and release reports); this test proves that the shared state machine
// and projection cannot skip a human gate or mislabel the terminal outcome.
func TestLocalDeliveryLifecycleTraversal(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	item := &models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StatePlanning, CreatedAt: now, UpdatedAt: now}

	advance := func(action deliveryworkflow.Action, gateKind string) {
		t.Helper()
		var gate *models.DeliveryGate
		if gateKind != "" {
			gate = &models.DeliveryGate{ID: uuid.Must(uuid.NewV4()), WorkItemID: item.ID, Kind: gateKind, Decision: deliveryworkflow.DecisionApproved, DecidedBy: "local-reviewer", DecidedAt: now}
		}
		if err := deliveryworkflow.Advance(item, action, gate, now); err != nil {
			t.Fatalf("advance %s: %v", action, err)
		}
		if gate != nil {
			item.Gates = append(item.Gates, *gate)
		}
		item.UpdatedAt = now
	}
	assertProjection := func(state, stage, kind string, actionID string) {
		t.Helper()
		projection := buildDeliveryWorkflowProjection(*item, now)
		if projection.State != state || projection.Stage != stage || projection.StateKind != kind {
			t.Fatalf("projection for %s = state=%q stage=%q kind=%q", state, projection.State, projection.Stage, projection.StateKind)
		}
		if actionID != "" && !hasLifecycleProjectionAction(projection.AvailableActions, actionID) {
			t.Fatalf("projection for %s omitted action %q: %#v", state, actionID, projection.AvailableActions)
		}
	}

	advance(deliveryworkflow.ActionSubmitPlan, "")
	assertProjection(deliveryworkflow.StatePlanReview, "plan", "review", "approve_plan")
	advance(deliveryworkflow.ActionApprovePlan, deliveryworkflow.GatePlan)
	assertProjection(deliveryworkflow.StateImplementation, "build", "waiting", "start_implementation")
	advance(deliveryworkflow.ActionSubmitCodeReview, "")
	assertProjection(deliveryworkflow.StateCodeReview, "build", "review", "approve_code_review")
	advance(deliveryworkflow.ActionApproveCodeReview, deliveryworkflow.GateCodeReview)
	assertProjection(deliveryworkflow.StatePreviewPending, "preview", "waiting", "")
	advance(deliveryworkflow.ActionPreviewReady, "")
	assertProjection(deliveryworkflow.StateQARunning, "qa", "waiting", "start_qa")
	advance(deliveryworkflow.ActionSubmitQA, "")
	assertProjection(deliveryworkflow.StateQAReview, "qa", "review", "approve_qa")
	advance(deliveryworkflow.ActionApproveQA, deliveryworkflow.GateQAReview)
	assertProjection(deliveryworkflow.StateReleaseReview, "release", "review", "start_summary")
	advance(deliveryworkflow.ActionApproveRelease, deliveryworkflow.GateRelease)
	assertProjection(deliveryworkflow.StateReleased, "release", "terminal", "")

	if item.State != deliveryworkflow.StateReleased {
		t.Fatalf("lifecycle finished in %q, want released", item.State)
	}
	if len(item.Gates) != 4 {
		t.Fatalf("lifecycle recorded %d human gates, want 4", len(item.Gates))
	}
	if got := buildDeliveryWorkflowProjection(*item, now); got.CanContinue || len(got.AvailableActions) != 0 {
		t.Fatalf("released work must be terminal and actionless: %#v", got)
	}
}

func hasLifecycleProjectionAction(actions []deliveryWorkflowProjectionAction, id string) bool {
	for _, action := range actions {
		if action.ID == id {
			return true
		}
	}
	return false
}
