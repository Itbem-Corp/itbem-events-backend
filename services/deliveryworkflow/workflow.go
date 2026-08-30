// Package deliveryworkflow owns the human-gated lifecycle for ITBEM delivery
// work. It is intentionally pure business logic so controllers, agents and
// background workers cannot implement divergent transition rules.
package deliveryworkflow

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"events-stocks/models"
)

const (
	StatePlanning       = "planning"
	StatePlanReview     = "plan_review"
	StateImplementation = "implementation"
	StateCodeReview     = "code_review"
	StatePreviewPending = "preview_pending"
	StateQARunning      = "qa_running"
	StateQAReview       = "qa_review"
	StateReleaseReview  = "release_review"
	StateReleased       = "released"
	StateBlocked        = "blocked"
	StateCancelled      = "cancelled"

	GatePlan       = "plan"
	GateCodeReview = "code_review"
	GateQAReview   = "qa_review"
	GateRelease    = "release"

	DecisionApproved         = "approved"
	DecisionChangesRequested = "changes_requested"
)

type Action string

const (
	ActionSubmitPlan         Action = "submit_plan"
	ActionApprovePlan        Action = "approve_plan"
	ActionRequestPlanChanges Action = "request_plan_changes"
	ActionSubmitCodeReview   Action = "submit_code_review"
	ActionApproveCodeReview  Action = "approve_code_review"
	ActionRequestCodeChanges Action = "request_code_changes"
	ActionPreviewReady       Action = "preview_ready"
	ActionSubmitQA           Action = "submit_qa"
	ActionApproveQA          Action = "approve_qa"
	ActionRequestQAChanges   Action = "request_qa_changes"
	ActionApproveRelease     Action = "approve_release"
	ActionBlock              Action = "block"
	ActionCancel             Action = "cancel"
)

type transition struct {
	from     string
	to       string
	gateKind string
	decision string
}

var transitions = map[Action]transition{
	ActionSubmitPlan:         {from: StatePlanning, to: StatePlanReview},
	ActionApprovePlan:        {from: StatePlanReview, to: StateImplementation, gateKind: GatePlan, decision: DecisionApproved},
	ActionRequestPlanChanges: {from: StatePlanReview, to: StatePlanning, gateKind: GatePlan, decision: DecisionChangesRequested},
	ActionSubmitCodeReview:   {from: StateImplementation, to: StateCodeReview},
	// A code approval authorizes deployment only to the controlled preview or
	// staging environment. The deploy adapter must report a ready preview before
	// QA can begin; production release still requires the final gate.
	ActionApproveCodeReview:  {from: StateCodeReview, to: StatePreviewPending, gateKind: GateCodeReview, decision: DecisionApproved},
	ActionRequestCodeChanges: {from: StateCodeReview, to: StateImplementation, gateKind: GateCodeReview, decision: DecisionChangesRequested},
	ActionPreviewReady:       {from: StatePreviewPending, to: StateQARunning},
	ActionSubmitQA:           {from: StateQARunning, to: StateQAReview},
	ActionApproveQA:          {from: StateQAReview, to: StateReleaseReview, gateKind: GateQAReview, decision: DecisionApproved},
	ActionRequestQAChanges:   {from: StateQAReview, to: StateImplementation, gateKind: GateQAReview, decision: DecisionChangesRequested},
	ActionApproveRelease:     {from: StateReleaseReview, to: StateReleased, gateKind: GateRelease, decision: DecisionApproved},
}

// Advance applies one permitted transition. Gates are required for every
// approval or rejection that can unlock or send work back to an agent.
func Advance(item *models.DeliveryWorkItem, action Action, gate *models.DeliveryGate, now time.Time) error {
	if item == nil {
		return errors.New("delivery work item is required")
	}
	state := strings.TrimSpace(item.State)
	if state == "" {
		state = StatePlanning
	}

	if action == ActionBlock {
		if isTerminal(state) {
			return fmt.Errorf("cannot block terminal work item in state %q", state)
		}
		item.State = StateBlocked
		return nil
	}
	if action == ActionCancel {
		if isTerminal(state) {
			return fmt.Errorf("cannot cancel terminal work item in state %q", state)
		}
		item.State = StateCancelled
		return nil
	}

	rule, ok := transitions[action]
	if !ok {
		return fmt.Errorf("unsupported delivery action %q", action)
	}
	if state != rule.from {
		return fmt.Errorf("action %q is not allowed from state %q", action, state)
	}
	if err := validateGate(item, gate, rule, now); err != nil {
		return err
	}
	item.State = rule.to
	return nil
}

func isTerminal(state string) bool {
	return state == StateReleased || state == StateCancelled
}

func validateGate(item *models.DeliveryWorkItem, gate *models.DeliveryGate, rule transition, now time.Time) error {
	if rule.gateKind == "" {
		if gate != nil {
			return errors.New("this delivery transition does not accept a gate decision")
		}
		return nil
	}
	if gate == nil {
		return fmt.Errorf("human %s gate is required", rule.gateKind)
	}
	if gate.WorkItemID != item.ID {
		return errors.New("gate does not belong to the delivery work item")
	}
	if strings.TrimSpace(gate.Kind) != rule.gateKind || strings.TrimSpace(gate.Decision) != rule.decision {
		return fmt.Errorf("transition requires %s gate with %s decision", rule.gateKind, rule.decision)
	}
	if strings.TrimSpace(gate.DecidedBy) == "" {
		return errors.New("human gate decider is required")
	}
	if gate.DecidedAt.IsZero() {
		gate.DecidedAt = now.UTC()
	}
	return nil
}
