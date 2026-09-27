package automationagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"events-stocks/models"
)

func newLeaseFencingRegressionStep(t *testing.T) (*planStepExecution, *planStepTestCallback) {
	t.Helper()
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	worker := planStepTestWorker(callback)
	step, available, err := claimNextPlanStepExecution(context.Background(), worker, stepCallbackUUID(), stepCallbackUUID(), steps)
	if err != nil || !available {
		t.Fatalf("claim failed: available=%v err=%v", available, err)
	}
	stopPlanStepKeeperForTest(t, step)
	// Keep the manager active without a background keeper so each test can
	// deterministically invalidate the fence at its chosen boundary.
	step.ctx, step.cancel = context.WithCancel(context.Background())
	return step, callback
}

// A cancelled execution context is the worker's local fence after ownership is
// lost. Verify that the activity/evidence boundary honors it as well as the
// terminal status callback boundary.
func TestLostPlanStepFenceStopsActivityEvidenceAndCompletion(t *testing.T) {
	step, callback := newLeaseFencingRegressionStep(t)
	step.lose(ErrPlanStepExecutionLost)
	if !errors.Is(step.Lost(), ErrPlanStepExecutionLost) {
		t.Fatalf("lost fence was not terminal: %v", step.Lost())
	}

	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	saves, evidenceWrites := 0, 0
	recorder, err := newStepActivityRecorder(callback, step, checkpoint, func() error {
		saves++
		return nil
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	operationErr, reportErr := runStepActivity(
		withStepActivityRecorder(step.Context(), recorder),
		"evidence", "acceptance_evidence", func() error {
			evidenceWrites++
			return nil
		},
	)
	if operationErr != nil || !errors.Is(reportErr, ErrPlanStepExecutionLost) {
		t.Fatalf("stale evidence operation should stop on the lost fence: operation=%v report=%v", operationErr, reportErr)
	}

	if err := step.Complete(context.Background()); !errors.Is(err, ErrPlanStepExecutionLost) {
		t.Fatalf("completion after fence loss = %v, want ErrPlanStepExecutionLost", err)
	}

	callback.mu.Lock()
	defer callback.mu.Unlock()
	if evidenceWrites != 0 || len(callback.activityRequests) != 0 || saves != 0 {
		t.Fatalf("lost fence allowed stale evidence side effects: operation=%d activity_callbacks=%d checkpoint_saves=%d", evidenceWrites, len(callback.activityRequests), saves)
	}
	if len(callback.statusRequests) != 0 {
		t.Fatalf("lost fence posted terminal status under stale identity: %#v", callback.statusRequests)
	}
}

func TestExpiredPlanStepFenceSuppressesActivityAndEvidence(t *testing.T) {
	step, callback := newLeaseFencingRegressionStep(t)
	step.mu.Lock()
	step.leaseExpiresAt = time.Now().UTC().Add(-time.Second)
	step.mu.Unlock()
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	saves, operations := 0, 0
	recorder, err := newStepActivityRecorder(callback, step, checkpoint, func() error {
		saves++
		return nil
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	operationErr, reportErr := runStepActivity(
		withStepActivityRecorder(step.Context(), recorder),
		"evidence", "acceptance_evidence", func() error {
			operations++
			return nil
		},
	)
	if operationErr != nil || !errors.Is(reportErr, ErrPlanStepExecutionLost) {
		t.Fatalf("expired fence was not rejected: operation=%v report=%v", operationErr, reportErr)
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if operations != 0 || len(callback.activityRequests) != 0 || saves != 0 {
		t.Fatalf("expired fence allowed evidence side effects: operations=%d activity_callbacks=%d saves=%d", operations, len(callback.activityRequests), saves)
	}
}

func TestLeaseLossDuringCheckpointSaveStopsBeforeActivityCallback(t *testing.T) {
	step, callback := newLeaseFencingRegressionStep(t)
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	saves, operations := 0, 0
	recorder, err := newStepActivityRecorder(callback, step, checkpoint, func() error {
		saves++
		if saves == 1 {
			step.lose(ErrPlanStepExecutionLost)
		}
		return nil
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	operationErr, reportErr := runStepActivity(
		withStepActivityRecorder(step.Context(), recorder),
		"tool", "agent_action", func() error {
			operations++
			return nil
		},
	)
	if operationErr != nil || !errors.Is(reportErr, ErrPlanStepExecutionLost) {
		t.Fatalf("save-time lease loss was not returned: operation=%v report=%v", operationErr, reportErr)
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if operations != 0 || len(callback.activityRequests) != 0 || saves != 1 {
		t.Fatalf("lease loss during checkpoint save continued work: operations=%d activity_callbacks=%d saves=%d", operations, len(callback.activityRequests), saves)
	}
}

func TestLeaseLossDuringOperationSuppressesFollowupEvidence(t *testing.T) {
	step, callback := newLeaseFencingRegressionStep(t)
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	saves, operations, detailsCalls := 0, 0, 0
	recorder, err := newStepActivityRecorder(callback, step, checkpoint, func() error {
		saves++
		return nil
	}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	operationErr, reportErr := runStepActivityWithDetails(
		withStepActivityRecorder(step.Context(), recorder),
		"evidence", "acceptance_evidence", func(string) *models.DeliveryPlanStepActivityDetails {
			detailsCalls++
			return nil
		}, func() error {
			operations++
			step.lose(ErrPlanStepExecutionLost)
			return nil
		},
	)
	if operationErr != nil || !errors.Is(reportErr, ErrPlanStepExecutionLost) {
		t.Fatalf("operation-time lease loss was not returned: operation=%v report=%v", operationErr, reportErr)
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if operations != 1 || detailsCalls != 0 || len(callback.activityRequests) != 1 || callback.activityRequests[0].Phase != "started" || saves != 2 {
		t.Fatalf("operation-time lease loss persisted follow-up evidence: operations=%d details=%d activities=%#v saves=%d", operations, detailsCalls, callback.activityRequests, saves)
	}
}

func TestLeaseLossDuringActivationCheckpointSaveStopsClaimedStep(t *testing.T) {
	step, callback := newLeaseFencingRegressionStep(t)
	worker := planStepTestWorker(callback)
	checkpoint := &agentCheckpoint{}
	saves := 0
	err := activateClaimedPlanStep(context.Background(), worker, step.lease.TaskID, step.lease.RunID, checkpoint, step, func() error {
		saves++
		step.lose(ErrPlanStepExecutionLost)
		return nil
	})
	if !errors.Is(err, ErrPlanStepExecutionLost) {
		t.Fatalf("activation continued after losing its lease while saving: %v", err)
	}
	if saves != 1 {
		t.Fatalf("activation checkpoint save count = %d, want 1", saves)
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.statusRequests) != 0 || len(callback.activityRequests) != 0 {
		t.Fatalf("activation posted stale step callbacks: statuses=%#v activities=%#v", callback.statusRequests, callback.activityRequests)
	}
}
