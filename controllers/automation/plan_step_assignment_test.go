package automation

import (
	"strings"
	"testing"

	"events-stocks/internal/automationagent"
	"events-stocks/models"

	"github.com/gofrs/uuid"
)

func validPlanStepAssignmentFixture() (models.DeliveryPlanStepAssignment, models.DeliveryPlanExecution, models.AutomationTask, models.AutomationTask, models.DeliveryPlan, uuid.UUID, string) {
	workItemID := uuid.Must(uuid.NewV4())
	parentID, childID, executionID, planID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	stepID, gateID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	machineID := uuid.Must(uuid.NewV4()).String()
	parent := models.AutomationTask{ID: parentID, Operation: "delivery.implementation", Status: "queued", DeliveryWorkItemID: &workItemID}
	child := models.AutomationTask{ID: childID, Operation: "delivery.implementation", Status: "running", DeliveryWorkItemID: &workItemID, AgentKey: "generalist", MachineID: machineID}
	plan := models.DeliveryPlan{ID: planID, Version: 2, Status: "approved", ApprovedGateID: &gateID}
	execution := models.DeliveryPlanExecution{
		ID: executionID, AutomationTaskID: parentID, PlanID: planID, PlanVersion: 2,
		ApprovedGateID: gateID, PlanHash: strings.Repeat("a", 64), Status: models.DeliveryPlanExecutionRunning,
	}
	assignment := models.DeliveryPlanStepAssignment{
		ID: uuid.Must(uuid.NewV4()), ExecutionID: executionID, DeliveryPlanStepID: stepID,
		ChildAutomationTaskID: childID, TargetMachineID: machineID, TargetAgentKey: "generalist", Status: models.DeliveryPlanStepAssignmentQueued,
	}
	return assignment, execution, parent, child, plan, stepID, execution.PlanHash
}

func TestPlanStepAssignmentRequiresExactFrozenParentChildAndApprovedPlan(t *testing.T) {
	assignment, execution, parent, child, plan, targetStepID, hash := validPlanStepAssignmentFixture()
	if !planStepAssignmentAllowsClaim(assignment, execution, parent, child, plan, targetStepID, hash) {
		t.Fatal("valid child assignment should be claimable")
	}
	tests := []struct {
		name   string
		mutate func(*models.DeliveryPlanStepAssignment, *models.DeliveryPlanExecution, *models.AutomationTask, *models.AutomationTask, *models.DeliveryPlan, *uuid.UUID, *string)
	}{
		{name: "wrong target", mutate: func(_ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, target *uuid.UUID, _ *string) {
			*target = uuid.Must(uuid.NewV4())
		}},
		{name: "different child task", mutate: func(_ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, child *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			child.ID = uuid.Must(uuid.NewV4())
		}},
		{name: "wrong execution plan", mutate: func(_ *models.DeliveryPlanStepAssignment, execution *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			execution.PlanID = uuid.Must(uuid.NewV4())
		}},
		{name: "stale plan version", mutate: func(_ *models.DeliveryPlanStepAssignment, execution *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			execution.PlanVersion++
		}},
		{name: "changed approved gate", mutate: func(_ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, plan *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			gate := uuid.Must(uuid.NewV4())
			plan.ApprovedGateID = &gate
		}},
		{name: "changed plan content", mutate: func(_ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, hash *string) {
			*hash = strings.Repeat("b", 64)
		}},
		{name: "cancelled parent", mutate: func(_ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, parent *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			parent.Status = "cancelled"
		}},
		{name: "terminal assignment", mutate: func(assignment *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			assignment.Status = models.DeliveryPlanStepAssignmentCompleted
		}},
		{name: "wrong assigned machine", mutate: func(assignment *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			assignment.TargetMachineID = uuid.Must(uuid.NewV4()).String()
		}},
		{name: "wrong assigned profile", mutate: func(assignment *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *models.AutomationTask, _ *models.AutomationTask, _ *models.DeliveryPlan, _ *uuid.UUID, _ *string) {
			assignment.TargetAgentKey = "backend-engineer"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedAssignment, changedExecution, changedParent, changedChild, changedPlan, changedTarget, changedHash := assignment, execution, parent, child, plan, targetStepID, hash
			test.mutate(&changedAssignment, &changedExecution, &changedParent, &changedChild, &changedPlan, &changedTarget, &changedHash)
			if planStepAssignmentAllowsClaim(changedAssignment, changedExecution, changedParent, changedChild, changedPlan, changedTarget, changedHash) {
				t.Fatal("invalid assignment must not authorize a targeted step claim")
			}
		})
	}
}

func TestPlanStepAssignmentLifecycleProjectionIsBounded(t *testing.T) {
	tests := []struct {
		stepStatus       string
		assignmentStatus string
		terminal         bool
	}{
		{stepStatus: models.DeliveryPlanStepRunning, assignmentStatus: models.DeliveryPlanStepAssignmentRunning},
		{stepStatus: models.DeliveryPlanStepCompleted, assignmentStatus: models.DeliveryPlanStepAssignmentCompleted, terminal: true},
		{stepStatus: models.DeliveryPlanStepFailed, assignmentStatus: models.DeliveryPlanStepAssignmentFailed, terminal: true},
		{stepStatus: models.DeliveryPlanStepBlocked, assignmentStatus: models.DeliveryPlanStepAssignmentBlocked},
	}
	for _, test := range tests {
		status, terminal := assignmentStatusForStepStatus(test.stepStatus)
		if status != test.assignmentStatus || terminal != test.terminal {
			t.Errorf("assignmentStatusForStepStatus(%q) = %q, %t; want %q, %t", test.stepStatus, status, terminal, test.assignmentStatus, test.terminal)
		}
	}
	if status, terminal := assignmentStatusForStepStatus(models.DeliveryPlanStepSkipped); status != "" || terminal {
		t.Fatalf("unsupported worker transition must not project an assignment state: %q, %t", status, terminal)
	}
}

func TestPlanStepAssignmentTargetUsesStableMachineAcrossWorkerRestart(t *testing.T) {
	assignment, _, _, _, _, _, _ := validPlanStepAssignmentFixture()
	firstProcess := automationagent.AgentIdentity{WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: assignment.TargetAgentKey, MachineID: assignment.TargetMachineID}
	if !planStepAssignmentTargetIdentityMatches(assignment, firstProcess) {
		t.Fatal("targeted machine/profile should match the first worker process")
	}
	newProcessAfterRestart := firstProcess
	newProcessAfterRestart.WorkerID = uuid.Must(uuid.NewV4()).String()
	if !planStepAssignmentTargetIdentityMatches(assignment, newProcessAfterRestart) {
		t.Fatal("replacement process on the same stable machine/profile should recover the assignment")
	}
	wrongMachine := newProcessAfterRestart
	wrongMachine.MachineID = uuid.Must(uuid.NewV4()).String()
	if planStepAssignmentTargetIdentityMatches(assignment, wrongMachine) {
		t.Fatal("another machine must not claim this persisted assignment")
	}
}

func TestTerminalPlanStepTransitionReplayRequiresExactPersistedTuple(t *testing.T) {
	assignment, execution, parent, child, plan, stepID, _ := validPlanStepAssignmentFixture()
	plan.WorkItemID = *child.DeliveryWorkItemID
	workerID, machineID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.FromString(assignment.TargetMachineID))
	runID := uuid.Must(uuid.NewV4()).String()
	fence := int64(7)
	completedAtTaskID := child.ID
	step := models.DeliveryPlanStep{
		ID: stepID, PlanID: plan.ID, Status: models.DeliveryPlanStepCompleted,
		AutomationTaskID: &completedAtTaskID, RunID: runID, WorkerID: workerID.String(),
		AgentKey: "generalist", MachineID: machineID.String(), LeaseFence: fence,
	}
	child.Status = "completed"
	assignment.Status = models.DeliveryPlanStepAssignmentCompleted
	identity := automationagent.AgentIdentity{WorkerID: workerID.String(), AgentKey: "generalist", MachineID: machineID.String()}

	if !terminalPlanStepReplayMatches(step, child, &parent, plan, &assignment, &execution, child.ID, runID, identity, fence, models.DeliveryPlanStepCompleted) {
		t.Fatal("exact persisted terminal assignment should accept an idempotent replay")
	}
	if !terminalPlanStepReplayMatches(step, child, nil, plan, nil, nil, child.ID, runID, identity, fence, models.DeliveryPlanStepCompleted) {
		t.Fatal("legacy terminal step should accept only its exact persisted lease tuple")
	}
	tests := []struct {
		name      string
		mutate    func(*models.DeliveryPlanStep, *models.AutomationTask, *models.DeliveryPlanStepAssignment, *models.DeliveryPlanExecution, *automationagent.AgentIdentity)
		wantMatch bool
	}{
		{name: "wrong fence", mutate: func(_ *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
		}, wantMatch: false},
		{name: "wrong worker", mutate: func(s *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
			s.WorkerID = uuid.Must(uuid.NewV4()).String()
		}},
		{name: "wrong run", mutate: func(s *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
			s.RunID = uuid.Must(uuid.NewV4()).String()
		}},
		{name: "wrong machine", mutate: func(s *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
			s.MachineID = uuid.Must(uuid.NewV4()).String()
		}},
		{name: "wrong agent profile", mutate: func(_ *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, i *automationagent.AgentIdentity) {
			i.AgentKey = "reviewer"
		}},
		{name: "wrong task", mutate: func(_ *models.DeliveryPlanStep, task *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
			task.ID = uuid.Must(uuid.NewV4())
		}},
		{name: "wrong assignment status", mutate: func(_ *models.DeliveryPlanStep, _ *models.AutomationTask, a *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
			a.Status = models.DeliveryPlanStepAssignmentFailed
		}},
		{name: "wrong execution plan", mutate: func(_ *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, e *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
			e.PlanID = uuid.Must(uuid.NewV4())
		}},
		{name: "wrong terminal status", mutate: func(_ *models.DeliveryPlanStep, _ *models.AutomationTask, _ *models.DeliveryPlanStepAssignment, _ *models.DeliveryPlanExecution, _ *automationagent.AgentIdentity) {
		}, wantMatch: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedStep, changedTask, changedAssignment, changedExecution, changedIdentity := step, child, assignment, execution, identity
			changedFence, changedStatus := fence, models.DeliveryPlanStepCompleted
			switch test.name {
			case "wrong fence":
				changedFence++
			case "wrong terminal status":
				changedStatus = models.DeliveryPlanStepFailed
			}
			test.mutate(&changedStep, &changedTask, &changedAssignment, &changedExecution, &changedIdentity)
			matched := terminalPlanStepReplayMatches(changedStep, changedTask, &parent, plan, &changedAssignment, &changedExecution, child.ID, runID, changedIdentity, changedFence, changedStatus)
			if matched != test.wantMatch {
				t.Fatalf("terminal replay match = %t, want %t", matched, test.wantMatch)
			}
		})
	}
}
