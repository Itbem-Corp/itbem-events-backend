package delivery

import (
	"events-stocks/models"
	"events-stocks/services/deliveryworkflow"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestBuildDeliveryWorkflowProjectionUsesDurableStateAndActiveTask(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	item := models.DeliveryWorkItem{
		ID:            uuid.Must(uuid.NewV4()),
		State:         deliveryworkflow.StateImplementation,
		AgentProgress: "queued",
		UpdatedAt:     now.Add(-2 * time.Minute),
		Evidence:      []models.DeliveryEvidence{{Kind: "test_result", Reference: "s3://evidence/test.json"}},
		ChangeSets:    []models.DeliveryChangeSet{{RepositoryRef: "workspace://backend"}},
		Gates:         []models.DeliveryGate{{Kind: deliveryworkflow.GatePlan, Decision: deliveryworkflow.DecisionApproved}},
		AutomationTasks: []models.AutomationTask{
			{ID: uuid.Must(uuid.NewV4()), Operation: "delivery.implementation", Status: "running", Provider: "minimax", Model: "MiniMax-M3", CreatedAt: now.Add(-5 * time.Minute), UpdatedAt: now.Add(-30 * time.Second)},
			{ID: uuid.Must(uuid.NewV4()), Operation: "delivery.plan", Status: "completed", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-6 * time.Minute)},
		},
	}
	projection := buildDeliveryWorkflowProjection(item, now)
	if projection.SchemaVersion != 2 || projection.Stage != "build" || projection.StateKind != "active" {
		t.Fatalf("unexpected projection identity: %#v", projection)
	}
	if projection.CurrentOperation != "delivery.implementation" || projection.Actor.Provider != "minimax" || projection.Actor.Model != "MiniMax-M3" {
		t.Fatalf("active task must be the source of current operation: %#v", projection)
	}
	if projection.Evidence.Total != 1 || projection.Evidence.Validations != 1 || !projection.Evidence.HasChanges || !projection.Evidence.HasHumanGate {
		t.Fatalf("evidence summary lost durable records: %#v", projection.Evidence)
	}
	if len(projection.AvailableActions) == 0 || projection.AvailableActions[0].ID != "cancel_task" {
		t.Fatalf("active work must expose cancellation before navigation: %#v", projection.AvailableActions)
	}
}

func TestBuildDeliveryWorkflowProjectionGuidesRecoveryByCause(t *testing.T) {
	item := models.DeliveryWorkItem{
		ID:    uuid.Must(uuid.NewV4()),
		State: deliveryworkflow.StatePlanning,
		AutomationTasks: []models.AutomationTask{{
			ID: uuid.Must(uuid.NewV4()), Operation: "delivery.plan", Status: "failed", ErrorMessage: "project budget reservation expired",
		}},
	}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.Recovery == nil || projection.Recovery.Mode != "budget" || projection.Recovery.ActionID != "open_costs" || !projection.Recovery.RequiresHumanReview {
		t.Fatalf("budget failure must produce explicit recovery guidance: %#v", projection.Recovery)
	}
	if projection.StateKind != "attention" || projection.CanContinue == false || projection.Summary != "Intento por revisar" {
		t.Fatalf("a bounded budget failure should be visibly recoverable: %#v", projection)
	}
}

func TestBuildDeliveryWorkflowProjectionClassifiesInvalidCodeReviewContract(t *testing.T) {
	item := models.DeliveryWorkItem{
		ID:    uuid.Must(uuid.NewV4()),
		State: deliveryworkflow.StateCodeReview,
		AutomationTasks: []models.AutomationTask{{
			ID: uuid.Must(uuid.NewV4()), Operation: "code.review", Status: "failed", ErrorMessage: "code review findings must not repeat a source location",
		}},
	}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.Recovery == nil || projection.Recovery.ReasonCode != "review_contract_invalid" || projection.Recovery.Mode != "repair" || projection.Recovery.ActionID != "open_activity" || !projection.Recovery.RequiresHumanReview {
		t.Fatalf("invalid reviewer contract must have structured recovery guidance: %#v", projection.Recovery)
	}
	if projection.Recovery.Title != "La revisión devolvió un formato inválido" {
		t.Fatalf("invalid reviewer contract copy drifted: %#v", projection.Recovery)
	}
}

func TestBuildDeliveryWorkflowProjectionGuidesBlockedContextWithoutRetry(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StateBlocked, AgentProgress: "blocked", BlockedReason: "Falta el repositorio aprobado."}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.Recovery == nil || projection.Recovery.Mode != "operator_input" || projection.Recovery.ActionID != "send_context" || projection.Recovery.Detail != item.BlockedReason {
		t.Fatalf("blocked work must guide the operator to context: %#v", projection.Recovery)
	}
	for _, action := range projection.AvailableActions {
		if action.Kind == "agent_run" {
			t.Fatalf("blocked work must not expose an automatic run: %#v", projection.AvailableActions)
		}
	}
}

func TestBuildDeliveryWorkflowProjectionExplainsQueuedResume(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StatePlanning, AgentProgress: "queued"}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.Recovery == nil || projection.Recovery.Mode != "resume" || projection.Recovery.ActionID != "open_activity" || projection.Recovery.RequiresHumanReview {
		t.Fatalf("queued work must explain durable continuation without asking for a duplicate run: %#v", projection.Recovery)
	}
}

func TestBuildDeliveryWorkflowProjectionDoesNotOfferRetryForAmbiguousFailure(t *testing.T) {
	item := models.DeliveryWorkItem{
		ID:    uuid.Must(uuid.NewV4()),
		State: deliveryworkflow.StatePlanning,
		AutomationTasks: []models.AutomationTask{{
			ID: uuid.Must(uuid.NewV4()), Operation: "delivery.plan", Status: "failed", ErrorMessage: "provider response storage unavailable; result uncertain",
		}},
	}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.StateKind != "uncertain" || projection.CanContinue {
		t.Fatalf("ambiguous failure must fail closed: %#v", projection)
	}
	for _, action := range projection.AvailableActions {
		if action.Kind == "agent_run" {
			t.Fatalf("must not offer a retry while outcome is uncertain: %#v", projection.AvailableActions)
		}
	}
}

func TestBuildDeliveryWorkflowProjectionExposesHumanGateActions(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StateCodeReview}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.StateKind != "review" || projection.Actor.Type != "human" || projection.WaitingCategory != "human_review" {
		t.Fatalf("code review must be human-owned: %#v", projection)
	}
	seenApprove, seenChanges := false, false
	for _, action := range projection.AvailableActions {
		if action.ID == "approve_code_review" {
			seenApprove = action.RequiresConfirmation && action.Transition == string(deliveryworkflow.ActionApproveCodeReview)
		}
		if action.ID == "request_code_changes" {
			seenChanges = action.RequiresConfirmation && action.Transition == string(deliveryworkflow.ActionRequestCodeChanges)
		}
	}
	if !seenApprove || !seenChanges {
		t.Fatalf("human gate actions must be explicit and confirmable: %#v", projection.AvailableActions)
	}
}

func TestBuildDeliveryWorkflowProjectionKeepsHumanGateSeparateFromMissingContext(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StatePlanReview, AgentProgress: "waiting_for_user"}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.WaitingCategory != "human_review" {
		t.Fatalf("review gate must not be relabeled as missing context: %#v", projection)
	}
	if projection.Recovery == nil || projection.Recovery.Mode != "human_review" || projection.Recovery.ActionID != "open_control" {
		t.Fatalf("review gate must point to the decision surface: %#v", projection.Recovery)
	}
	for _, action := range projection.AvailableActions {
		if action.ID == "send_context" {
			t.Fatalf("review gate must not imply a missing-context message: %#v", projection.AvailableActions)
		}
	}
}

func TestBuildDeliveryWorkflowProjectionDoesNotTurnInformationalChatIntoDeliveryAttention(t *testing.T) {
	now := time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC)
	item := models.DeliveryWorkItem{
		ID:            uuid.Must(uuid.NewV4()),
		State:         deliveryworkflow.StatePlanReview,
		AgentProgress: "waiting_for_user",
		AutomationTasks: []models.AutomationTask{
			{ID: uuid.Must(uuid.NewV4()), Operation: "delivery.chat", Status: "failed", ErrorMessage: "optional chat suggestion was malformed", CreatedAt: now.Add(-time.Minute), UpdatedAt: now},
			{ID: uuid.Must(uuid.NewV4()), Operation: "delivery.plan", Status: "completed", CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-5 * time.Minute)},
		},
	}
	projection := buildDeliveryWorkflowProjection(item, now)
	if projection.StateKind != "review" || projection.Summary != "Plan por revisar" || projection.WaitingCategory != "human_review" {
		t.Fatalf("informational chat must not displace the durable plan gate: %#v", projection)
	}
	if projection.Recovery == nil || projection.Recovery.Mode != "human_review" || projection.Recovery.ActionID != "open_control" {
		t.Fatalf("plan gate recovery must remain available after a chat failure: %#v", projection.Recovery)
	}
}

func TestBuildDeliveryWorkflowProjectionReportsTerminalWithoutControls(t *testing.T) {
	item := models.DeliveryWorkItem{ID: uuid.Must(uuid.NewV4()), State: deliveryworkflow.StateReleased}
	projection := buildDeliveryWorkflowProjection(item, time.Now().UTC())
	if projection.StateKind != "terminal" || projection.Stage != "release" || projection.CanContinue || projection.StaleAfterSecs != 0 {
		t.Fatalf("released work must be terminal: %#v", projection)
	}
	for _, action := range projection.AvailableActions {
		if action.Kind == "agent_run" || action.Kind == "transition" || action.Kind == "task_cancel" {
			t.Fatalf("terminal work must not expose mutating controls: %#v", projection.AvailableActions)
		}
	}
}
