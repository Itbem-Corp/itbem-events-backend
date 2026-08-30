package deliveryworkflow

import (
	"strings"
	"testing"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestAdvanceRequiresHumanGatesAndPreservesOrder(t *testing.T) {
	item := &models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: StatePlanning}
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)

	if err := Advance(item, ActionSubmitPlan, nil, now); err != nil {
		t.Fatalf("submit plan: %v", err)
	}
	if err := Advance(item, ActionApprovePlan, nil, now); err == nil || !strings.Contains(err.Error(), "human plan gate") {
		t.Fatalf("plan approval without a human gate must fail, got %v", err)
	}
	if item.State != StatePlanReview {
		t.Fatalf("failed transition changed state to %q", item.State)
	}

	approvePlan(t, item, now)
	if item.State != StateImplementation {
		t.Fatalf("expected implementation after approved plan, got %q", item.State)
	}

	if err := Advance(item, ActionSubmitCodeReview, nil, now); err != nil {
		t.Fatalf("submit code review: %v", err)
	}
	approveGate(t, item, GateCodeReview, now)
	if item.State != StatePreviewPending {
		t.Fatalf("code approval must authorize only a controlled preview, got %q", item.State)
	}
	if err := Advance(item, ActionPreviewReady, nil, now); err != nil {
		t.Fatalf("mark preview ready: %v", err)
	}
	if item.State != StateQARunning {
		t.Fatalf("QA must start only after a ready preview, got %q", item.State)
	}

	if err := Advance(item, ActionSubmitQA, nil, now); err != nil {
		t.Fatalf("submit QA: %v", err)
	}
	approveGate(t, item, GateQAReview, now)
	if item.State != StateReleaseReview {
		t.Fatalf("QA approval must wait for release gate, got %q", item.State)
	}
	approveGate(t, item, GateRelease, now)
	if item.State != StateReleased {
		t.Fatalf("release approval must finish the work item, got %q", item.State)
	}
}

func TestAdvanceRejectsSkippedOrMismatchedGate(t *testing.T) {
	item := &models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: StateCodeReview}
	now := time.Now().UTC()

	if err := Advance(item, ActionSubmitQA, nil, now); err == nil {
		t.Fatal("QA cannot start before human code review approval")
	}
	wrongGate := &models.DeliveryGate{WorkItemID: item.ID, Kind: GateQAReview, Decision: DecisionApproved, DecidedBy: "reviewer"}
	if err := Advance(item, ActionApproveCodeReview, wrongGate, now); err == nil {
		t.Fatal("mismatched gate must fail")
	}
	if item.State != StateCodeReview {
		t.Fatalf("failed transition changed state to %q", item.State)
	}
}

func approvePlan(t *testing.T, item *models.DeliveryWorkItem, now time.Time) {
	t.Helper()
	if err := Advance(item, ActionApprovePlan, &models.DeliveryGate{WorkItemID: item.ID, Kind: GatePlan, Decision: DecisionApproved, DecidedBy: "reviewer"}, now); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
}

func approveGate(t *testing.T, item *models.DeliveryWorkItem, kind string, now time.Time) {
	t.Helper()
	action := ActionApproveCodeReview
	if kind == GateQAReview {
		action = ActionApproveQA
	}
	if kind == GateRelease {
		action = ActionApproveRelease
	}
	if err := Advance(item, action, &models.DeliveryGate{WorkItemID: item.ID, Kind: kind, Decision: DecisionApproved, DecidedBy: "reviewer"}, now); err != nil {
		t.Fatalf("approve %s: %v", kind, err)
	}
}
