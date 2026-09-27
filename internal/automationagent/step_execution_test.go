package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

type planStepTestCallback struct {
	*fakeCallback
	mu                    sync.Mutex
	steps                 []PlanStepDTO
	statuses              map[string]string
	claimRequests         []PlanStepClaimRequest
	renewRequests         []PlanStepLeaseRequest
	statusRequests        []PlanStepStatusRequest
	activityRequests      []PlanStepActivityRequest
	activityStepIDs       []string
	activityErr           error
	dependencyManifest    PlanStepDependencyPatchManifest
	dependencyPatchBytes  map[string][]byte
	dependencyPatchErr    error
	dependencyRequests    []string
	renewErr              error
	renewResponses        []PlanStepLease
	renewBlock            <-chan struct{}
	renewStarted          chan struct{}
	claimMutation         func(PlanStepDTO) PlanStepDTO
	claimResponseMutation func(PlanStepClaim) PlanStepClaim
	claimCount            int
}

func (callback *planStepTestCallback) ClaimPlanStep(_ context.Context, request PlanStepClaimRequest) (PlanStepClaim, error) {
	callback.mu.Lock()
	defer callback.mu.Unlock()
	callback.claimRequests = append(callback.claimRequests, request)
	callback.claimCount++
	for _, step := range callback.steps {
		if request.StepID != "" && step.ID != request.StepID {
			continue
		}
		status := callback.statuses[step.ID]
		if status == "" {
			status = step.Status
		}
		if status != "planned" && status != "ready" {
			continue
		}
		ready := true
		for _, dependency := range step.DependsOn {
			for _, candidate := range callback.steps {
				if candidate.StepKey == dependency && callback.statuses[candidate.ID] != "completed" {
					ready = false
				}
			}
		}
		if !ready {
			continue
		}
		callback.statuses[step.ID] = "running"
		claimed := step
		claimed.Status = "running"
		claimed.AutomationTaskID = request.TaskID
		claimed.AgentKey = request.AgentKey
		if callback.claimMutation != nil {
			claimed = callback.claimMutation(claimed)
		}
		result := PlanStepClaim{Available: true, Step: &claimed, FencingToken: fmt.Sprintf("%d", callback.claimCount), LeaseExpiresAt: time.Now().Add(time.Minute)}
		if callback.claimResponseMutation != nil {
			result = callback.claimResponseMutation(result)
		}
		return result, nil
	}
	result := PlanStepClaim{}
	if callback.claimResponseMutation != nil {
		result = callback.claimResponseMutation(result)
	}
	return result, nil
}

func (callback *planStepTestCallback) RenewPlanStepLease(ctx context.Context, request PlanStepLeaseRequest) (PlanStepLease, error) {
	callback.mu.Lock()
	callback.renewRequests = append(callback.renewRequests, request)
	err := callback.renewErr
	block := callback.renewBlock
	started := callback.renewStarted
	var response PlanStepLease
	if len(callback.renewResponses) > 0 {
		response = callback.renewResponses[0]
		callback.renewResponses = callback.renewResponses[1:]
	} else {
		response = PlanStepLease{LeaseExpiresAt: time.Now().UTC().Add(time.Minute)}
	}
	callback.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return PlanStepLease{}, ctx.Err()
		case <-block:
		}
	}
	if err != nil {
		return PlanStepLease{}, err
	}
	return response, nil
}

func (callback *planStepTestCallback) UpdatePlanStepStatus(_ context.Context, request PlanStepStatusRequest) (PlanStepDTO, error) {
	callback.mu.Lock()
	defer callback.mu.Unlock()
	callback.statusRequests = append(callback.statusRequests, request)
	for _, step := range callback.steps {
		if step.ID == request.StepID {
			callback.statuses[step.ID] = request.Status
			step.Status = request.Status
			return step, nil
		}
	}
	return PlanStepDTO{}, errors.New("step missing")
}

func (callback *planStepTestCallback) GetPlanStepDependencyPatchManifest(_ context.Context, _ PlanStepLeaseRequest) (PlanStepDependencyPatchManifest, error) {
	callback.mu.Lock()
	defer callback.mu.Unlock()
	manifest := callback.dependencyManifest
	if manifest.ManifestSHA256 == "" {
		manifest.ManifestSHA256, _ = models.DeliveryPlanStepDependencyPatchManifestSHA256(nil)
	}
	manifest.Patches = append([]models.DeliveryPlanStepDependencyPatchReference(nil), manifest.Patches...)
	return manifest, callback.dependencyPatchErr
}

func (callback *planStepTestCallback) GetPlanStepDependencyPatch(_ context.Context, _ PlanStepLeaseRequest, digest string) ([]byte, error) {
	callback.mu.Lock()
	defer callback.mu.Unlock()
	callback.dependencyRequests = append(callback.dependencyRequests, digest)
	if callback.dependencyPatchErr != nil {
		return nil, callback.dependencyPatchErr
	}
	return append([]byte(nil), callback.dependencyPatchBytes[digest]...), nil
}

func (callback *planStepTestCallback) RecordPlanStepActivity(_ context.Context, stepID string, request PlanStepActivityRequest) error {
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if callback.activityErr != nil {
		return callback.activityErr
	}
	callback.activityStepIDs = append(callback.activityStepIDs, stepID)
	callback.activityRequests = append(callback.activityRequests, request)
	return nil
}

func planStepTestFixture() ([]PlanStepDTO, json.RawMessage) {
	planID := uuid.Must(uuid.NewV4()).String()
	stepOne := PlanStepDTO{
		ID: uuid.Must(uuid.NewV4()).String(), PlanID: planID, PlanVersion: 2,
		Role:    models.DeliveryPlanStepRoleImplementation,
		StepKey: "prepare", Order: 0, Title: "Prepare", Objective: "Create the first artifact",
		AcceptanceCriteria: []string{"First artifact exists"}, DependsOn: []string{}, Status: "planned",
	}
	stepTwo := PlanStepDTO{
		ID: uuid.Must(uuid.NewV4()).String(), PlanID: planID, PlanVersion: 2,
		Role:    models.DeliveryPlanStepRoleImplementation,
		StepKey: "verify", Order: 1, Title: "Verify", Objective: "Create the second artifact",
		AcceptanceCriteria: []string{"Second artifact exists"}, DependsOn: []string{"prepare"}, Status: "planned",
	}
	integration := PlanStepDTO{
		ID: uuid.Must(uuid.NewV4()).String(), PlanID: planID, PlanVersion: 2,
		Role:    models.DeliveryPlanStepRoleIntegration,
		StepKey: "integrate", Order: 2, Title: "Integrate", Objective: "Merge and verify all implementation branches",
		AcceptanceCriteria: []string{"First artifact exists", "Second artifact exists"}, DependsOn: []string{"verify"}, Status: "planned",
	}
	steps := []PlanStepDTO{stepOne, stepTwo, integration}
	encoded, _ := json.Marshal(map[string]any{
		"work_item":      map[string]any{"acceptance_criteria": []string{"First artifact exists", "Second artifact exists"}},
		"plan_steps":     steps,
		"plan_execution": planStepExecutionBindingForTest(steps),
	})
	return steps, encoded
}

func planStepExecutionBindingForTest(steps []PlanStepDTO) map[string]any {
	return map[string]any{
		"parent_task_id": "00000000-0000-4000-8000-000000000001",
		"plan_id":        steps[0].PlanID,
		"plan_version":   steps[0].PlanVersion,
		"plan_hash":      strings.Repeat("a", 64),
	}
}

func planStepTestWorker(callback *planStepTestCallback) *Worker {
	workerID := uuid.Must(uuid.NewV4()).String()
	machineID := uuid.Must(uuid.NewV4()).String()
	identity := AgentIdentity{WorkerID: workerID, AgentKey: "generalist", MachineID: machineID}
	return &Worker{
		config:   WorkerConfig{WorkerID: workerID, AgentKey: identity.AgentKey, MachineID: machineID},
		callback: identityTaskCallback{inner: callback, identity: identity},
	}
}

func newPlanStepTestCallback(steps []PlanStepDTO) *planStepTestCallback {
	return &planStepTestCallback{fakeCallback: &fakeCallback{}, steps: steps, statuses: map[string]string{}}
}

func TestParsePlanStepsMapsExactCriteriaAndAllowsDAG(t *testing.T) {
	steps, delivery := planStepTestFixture()
	parsed, criteria, err := parsePlanSteps(delivery)
	if err != nil || len(parsed) != 3 || len(criteria) != 2 {
		t.Fatalf("parsePlanSteps = %d steps, %v", len(parsed), err)
	}
	if err := validateStepAcceptanceMapping(steps[0].AcceptanceCriteria, criteria); !errors.Is(err, errPlanStepCriteriaMustCoverAll) {
		t.Fatalf("a valid per-node subset must be distinguishable from full-work coverage: %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(delivery, &envelope); err != nil {
		t.Fatal(err)
	}
	item := envelope["work_item"].(map[string]any)
	item["acceptance_criteria"] = []string{"First artifact exists", "Second artifact exists", "Unmapped criterion"}
	stepsJSON := envelope["plan_steps"].([]any)
	integration := stepsJSON[2].(map[string]any)
	integration["acceptance_criteria"] = []string{"First artifact exists", "Second artifact exists", "Unmapped criterion"}
	broken, _ := json.Marshal(envelope)
	if _, _, err := parsePlanSteps(broken); err == nil {
		t.Fatalf("unmapped global acceptance criterion was accepted: %v", err)
	}

	item["acceptance_criteria"] = []string{"First artifact exists", "Second artifact exists"}
	integration["acceptance_criteria"] = []string{"First artifact exists", "Second artifact exists"}
	first := stepsJSON[0].(map[string]any)
	first["acceptance_criteria"] = []string{"Invented criterion"}
	broken, _ = json.Marshal(envelope)
	if _, _, err := parsePlanSteps(broken); err == nil || !strings.Contains(err.Error(), "exactly match") {
		t.Fatalf("unknown step criterion was accepted: %v", err)
	}
}

func TestClaimNextPlanStepUsesWorkerRunAndProfileAndFollowsDependencies(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	worker := planStepTestWorker(callback)
	taskID, runID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()

	first, available, err := claimNextPlanStepExecution(context.Background(), worker, taskID, runID, steps)
	if err != nil || !available || first == nil || first.claim.Step.StepKey != "prepare" {
		t.Fatalf("first claim = %#v available=%v err=%v", first, available, err)
	}
	callback.mu.Lock()
	request := callback.claimRequests[0]
	callback.mu.Unlock()
	if request.TaskID != taskID || request.RunID != runID || request.WorkerID != worker.config.WorkerID || request.AgentKey != "generalist" || request.MachineID != worker.config.MachineID || request.PlanID != steps[0].PlanID {
		t.Fatalf("claim omitted the registered task/run/worker/profile/plan tuple: %#v", request)
	}
	if err := first.Complete(context.Background()); err != nil {
		t.Fatal("first step could not be completed by the current fenced owner")
	}
	second, available, err := claimNextPlanStepExecution(context.Background(), worker, taskID, runID, steps)
	if err != nil || !available || second == nil || second.claim.Step.StepKey != "verify" {
		t.Fatalf("dependent claim = %#v available=%v err=%v", second, available, err)
	}
	if err := second.Fail(context.Background(), "failed"); err != nil {
		t.Fatal(err)
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.statusRequests) != 2 || callback.statusRequests[0].Status != "completed" || callback.statusRequests[1].Status != "failed" {
		t.Fatalf("unexpected fenced status events: %#v", callback.statusRequests)
	}
	for _, update := range callback.statusRequests {
		if update.AgentKey != "generalist" || update.MachineID != worker.config.MachineID {
			t.Fatalf("status identity diverged from the claiming worker: %#v", update)
		}
	}
}

func TestIdentityTaskCallbackPinsStepLeaseAndStatusToRegisteredWorker(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	worker := planStepTestWorker(callback)
	identityCallback := worker.callback.(identityTaskCallback)
	identity := worker.identity()
	spoofedMachineID := uuid.Must(uuid.NewV4()).String()
	lease := PlanStepLeaseRequest{
		StepID: steps[0].ID, TaskID: uuid.Must(uuid.NewV4()).String(), RunID: uuid.Must(uuid.NewV4()).String(),
		WorkerID: uuid.Must(uuid.NewV4()).String(), AgentKey: "different_agent", MachineID: spoofedMachineID,
		FencingToken: "7", LeaseSeconds: 120,
	}
	if _, err := identityCallback.RenewPlanStepLease(context.Background(), lease); err != nil {
		t.Fatalf("lease renewal failed: %v", err)
	}
	status := PlanStepStatusRequest{
		StepID: lease.StepID, TaskID: lease.TaskID, RunID: lease.RunID, WorkerID: lease.WorkerID,
		AgentKey: lease.AgentKey, MachineID: lease.MachineID, FencingToken: lease.FencingToken, Status: "completed",
	}
	if _, err := identityCallback.UpdatePlanStepStatus(context.Background(), status); err != nil {
		t.Fatalf("step status update failed: %v", err)
	}

	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.renewRequests) != 1 || callback.renewRequests[0].WorkerID != identity.WorkerID || callback.renewRequests[0].AgentKey != identity.AgentKey || callback.renewRequests[0].MachineID != identity.MachineID {
		t.Fatalf("lease renewal was not bound to the registered process identity: %#v", callback.renewRequests)
	}
	if len(callback.statusRequests) != 1 || callback.statusRequests[0].WorkerID != identity.WorkerID || callback.statusRequests[0].AgentKey != identity.AgentKey || callback.statusRequests[0].MachineID != identity.MachineID {
		t.Fatalf("step status was not bound to the registered process identity: %#v", callback.statusRequests)
	}
}

func TestClaimPlanStepExecutionTargetsOnlyRequestedStep(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	callback.statuses[steps[0].ID] = "completed"
	worker := planStepTestWorker(callback)
	taskID, runID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()

	target, available, err := claimPlanStepExecution(context.Background(), worker, taskID, runID, steps, steps[1].ID)
	if err != nil || !available || target == nil || target.claim.Step.ID != steps[1].ID {
		t.Fatalf("targeted claim = %#v available=%v err=%v", target, available, err)
	}
	callback.mu.Lock()
	request := callback.claimRequests[0]
	callback.mu.Unlock()
	if request.StepID != steps[1].ID {
		t.Fatalf("claim did not include the requested step id: %#v", request)
	}
	if err := target.Fail(context.Background(), "failed"); err != nil {
		t.Fatal(err)
	}
}

func TestClaimPlanStepRejectsChangedImmutablePlanDefinition(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(PlanStepDTO) PlanStepDTO
	}{
		{name: "order", mutate: func(step PlanStepDTO) PlanStepDTO { step.Order++; return step }},
		{name: "title", mutate: func(step PlanStepDTO) PlanStepDTO { step.Title = "Different title"; return step }},
		{name: "objective", mutate: func(step PlanStepDTO) PlanStepDTO { step.Objective = "Different objective"; return step }},
		{name: "task assignment", mutate: func(step PlanStepDTO) PlanStepDTO {
			step.AutomationTaskID = uuid.Must(uuid.NewV4()).String()
			return step
		}},
		{name: "agent assignment", mutate: func(step PlanStepDTO) PlanStepDTO { step.AgentKey = "qa_specialist"; return step }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps, _ := planStepTestFixture()
			callback := newPlanStepTestCallback(steps)
			callback.claimMutation = test.mutate
			worker := planStepTestWorker(callback)
			_, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
			if err == nil || available {
				t.Fatalf("claim with a changed immutable plan definition was accepted: available=%v err=%v", available, err)
			}
			callback.mu.Lock()
			defer callback.mu.Unlock()
			if len(callback.renewRequests) != 0 {
				t.Fatalf("worker started lease renewal for a mismatched claim: %#v", callback.renewRequests)
			}
		})
	}
}

func TestClaimTargetRejectsDifferentReadyStepBeforeStartingLeaseKeeper(t *testing.T) {
	steps, _ := planStepTestFixture()
	// Model two independent, concurrently claimable siblings. The control plane
	// must honor a child task's exact target even when another sibling is ready.
	steps[1].DependsOn = []string{}
	callback := newPlanStepTestCallback(steps)
	callback.claimResponseMutation = func(claim PlanStepClaim) PlanStepClaim {
		if claim.Available {
			other := steps[0]
			other.Status = "running"
			other.AutomationTaskID = claim.Step.AutomationTaskID
			other.AgentKey = claim.Step.AgentKey
			claim.Step = &other
		}
		return claim
	}
	worker := planStepTestWorker(callback)
	taskID, runID := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()

	execution, available, err := claimPlanStepExecution(context.Background(), worker, taskID, runID, steps, steps[1].ID)
	if err == nil || available || execution != nil {
		t.Fatalf("claim for a different ready sibling was accepted: execution=%#v available=%v err=%v", execution, available, err)
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.renewRequests) != 0 {
		t.Fatalf("worker started lease renewal after receiving a different targeted step: %#v", callback.renewRequests)
	}
}

func TestClaimPlanStepFailsClosedOnInconsistentOrExpiredClaim(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(PlanStepClaim) PlanStepClaim
	}{
		{
			name: "available without step",
			mutate: func(claim PlanStepClaim) PlanStepClaim {
				claim.Step = nil
				return claim
			},
		},
		{
			name: "available without fence",
			mutate: func(claim PlanStepClaim) PlanStepClaim {
				claim.FencingToken = ""
				return claim
			},
		},
		{
			name: "available with noncanonical fence",
			mutate: func(claim PlanStepClaim) PlanStepClaim {
				claim.FencingToken = "fence-1"
				return claim
			},
		},
		{
			name: "available with expired lease",
			mutate: func(claim PlanStepClaim) PlanStepClaim {
				claim.LeaseExpiresAt = time.Now().UTC().Add(-time.Second)
				return claim
			},
		},
		{
			name: "unavailable with assignment",
			mutate: func(claim PlanStepClaim) PlanStepClaim {
				claim.Available = false
				return claim
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps, _ := planStepTestFixture()
			callback := newPlanStepTestCallback(steps)
			callback.claimResponseMutation = test.mutate
			worker := planStepTestWorker(callback)

			step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
			if err == nil || available || step != nil {
				t.Fatalf("malformed claim was treated as a valid assignment or empty queue: step=%#v available=%v err=%v", step, available, err)
			}
			callback.mu.Lock()
			defer callback.mu.Unlock()
			if len(callback.renewRequests) != 0 {
				t.Fatalf("worker started lease renewal for an invalid claim: %#v", callback.renewRequests)
			}
		})
	}
}

func TestClaimPlanStepAcceptsOnlyAnEmptyNoWorkResponse(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	for _, step := range steps {
		callback.statuses[step.ID] = "completed"
	}
	worker := planStepTestWorker(callback)

	execution, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
	if err != nil || available || execution != nil {
		t.Fatalf("empty no-work response was rejected: execution=%#v available=%v err=%v", execution, available, err)
	}
}

type fakePlanStepTimer struct {
	clock    *fakePlanStepClock
	ch       chan time.Time
	due      time.Time
	duration time.Duration
	stopped  bool
	fired    bool
}

func (timer *fakePlanStepTimer) Chan() <-chan time.Time { return timer.ch }
func (timer *fakePlanStepTimer) Stop() bool {
	timer.clock.mu.Lock()
	defer timer.clock.mu.Unlock()
	if timer.stopped || timer.fired {
		return false
	}
	timer.stopped = true
	return true
}

type fakePlanStepClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakePlanStepTimer
	changed chan struct{}
}

func newFakePlanStepClock(now time.Time) *fakePlanStepClock {
	return &fakePlanStepClock{now: now.UTC(), changed: make(chan struct{}, 1)}
}

func (clock *fakePlanStepClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakePlanStepClock) NewTimer(duration time.Duration) planStepTimer {
	clock.mu.Lock()
	timer := &fakePlanStepTimer{clock: clock, ch: make(chan time.Time, 1), due: clock.now.Add(duration), duration: duration}
	clock.timers = append(clock.timers, timer)
	if duration <= 0 {
		timer.fired = true
		timer.ch <- clock.now
	}
	clock.mu.Unlock()
	select {
	case clock.changed <- struct{}{}:
	default:
	}
	return timer
}

func (clock *fakePlanStepClock) waitForTimerCount(t *testing.T, count int) []*fakePlanStepTimer {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		clock.mu.Lock()
		if len(clock.timers) >= count {
			timers := append([]*fakePlanStepTimer(nil), clock.timers...)
			clock.mu.Unlock()
			return timers
		}
		clock.mu.Unlock()
		select {
		case <-clock.changed:
		case <-deadline:
			clock.mu.Lock()
			created := len(clock.timers)
			clock.mu.Unlock()
			t.Fatalf("lease keeper created %d timers, want %d", created, count)
		}
	}
}

func (clock *fakePlanStepClock) fire(t *testing.T, index int) {
	t.Helper()
	clock.mu.Lock()
	if index >= len(clock.timers) || index < 0 {
		clock.mu.Unlock()
		t.Fatalf("timer %d does not exist", index)
	}
	timer := clock.timers[index]
	if timer.stopped || timer.fired {
		clock.mu.Unlock()
		t.Fatalf("timer %d cannot be fired", index)
	}
	if clock.now.Before(timer.due) {
		clock.now = timer.due
	}
	timer.fired = true
	now := clock.now
	clock.mu.Unlock()
	timer.ch <- now
}

func stopPlanStepKeeperForTest(t *testing.T, step *planStepExecution) {
	t.Helper()
	step.cancel()
	<-step.done
}

func startFakePlanStepKeeper(step *planStepExecution, every time.Duration, clock planStepClock) {
	step.ctx, step.cancel = context.WithCancel(context.Background())
	step.done = make(chan struct{})
	go step.keepLease(every, clock)
}

func TestStepLeaseKeeperRenewsTaskAndStepWhileWorkIsRunning(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	worker := planStepTestWorker(callback)
	step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
	if err != nil || !available {
		t.Fatalf("claim failed: available=%v err=%v", available, err)
	}
	stopPlanStepKeeperForTest(t, step)
	step.claim.LeaseExpiresAt = time.Now().UTC().Add(time.Minute)
	callback.renewResponses = []PlanStepLease{{LeaseExpiresAt: step.claim.LeaseExpiresAt.Add(time.Minute)}}
	clock := newFakePlanStepClock(time.Now().UTC())
	startFakePlanStepKeeper(step, time.Second, clock)
	clock.waitForTimerCount(t, 2)
	clock.fire(t, 0)
	deadline := time.After(time.Second)
	for {
		callback.mu.Lock()
		renewed := len(callback.renewRequests)
		var leaseRequest PlanStepLeaseRequest
		if renewed > 0 {
			leaseRequest = callback.renewRequests[0]
		}
		callback.mu.Unlock()
		callback.fakeCallback.mu.Lock()
		updates := len(callback.fakeCallback.updates)
		callback.fakeCallback.mu.Unlock()
		if renewed == 1 && updates > 0 {
			if leaseRequest.AgentKey != "generalist" || leaseRequest.MachineID != worker.config.MachineID {
				t.Fatalf("lease renewal identity diverged from the claiming worker: %#v", leaseRequest)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("fake clock tick did not renew both the task and step leases")
		case <-time.After(time.Millisecond):
		}
	}
	step.cancel()
	<-step.done
}

func TestStepLeaseLossCancelsInFlightExecutionContext(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	worker := planStepTestWorker(callback)
	step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
	if err != nil || !available {
		t.Fatalf("claim failed: available=%v err=%v", available, err)
	}
	stopPlanStepKeeperForTest(t, step)
	callback.mu.Lock()
	callback.renewErr = ErrPlanStepLeaseLost
	callback.mu.Unlock()
	step.claim.LeaseExpiresAt = time.Now().UTC().Add(time.Minute)
	clock := newFakePlanStepClock(time.Now().UTC())
	startFakePlanStepKeeper(step, time.Second, clock)
	clock.waitForTimerCount(t, 2)
	clock.fire(t, 0)
	select {
	case <-step.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("lost fencing token did not cancel active work")
	}
	<-step.done
	if !errors.Is(step.Lost(), ErrPlanStepExecutionLost) {
		t.Fatalf("lost lease was not made terminal: %v", step.Lost())
	}
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.statusRequests) != 0 {
		t.Fatalf("stale lease attempted a status mutation: %#v", callback.statusRequests)
	}
}

func TestStepLeaseKeeperStopsWithoutMutationWhenCancelled(t *testing.T) {
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	worker := planStepTestWorker(callback)
	step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
	if err != nil || !available {
		t.Fatalf("claim failed: available=%v err=%v", available, err)
	}
	stopPlanStepKeeperForTest(t, step)
	step.claim.LeaseExpiresAt = time.Now().UTC().Add(time.Minute)
	clock := newFakePlanStepClock(time.Now().UTC())
	startFakePlanStepKeeper(step, time.Second, clock)
	clock.waitForTimerCount(t, 2)
	step.cancel()
	<-step.done
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.renewRequests) != 0 || len(callback.statusRequests) != 0 {
		t.Fatal("cancelled work renewed or changed a step")
	}
}

func TestStepLeaseKeeperSchedulesFromRenewedExpiry(t *testing.T) {
	origin := time.Now().UTC()
	expiry := origin.Add(time.Minute)
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	callback.claimResponseMutation = func(claim PlanStepClaim) PlanStepClaim {
		claim.LeaseExpiresAt = expiry
		return claim
	}
	worker := planStepTestWorker(callback)
	step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
	if err != nil || !available {
		t.Fatalf("claim failed: available=%v err=%v", available, err)
	}
	stopPlanStepKeeperForTest(t, step)
	callback.renewResponses = []PlanStepLease{{LeaseExpiresAt: origin.Add(70 * time.Second)}}
	clock := newFakePlanStepClock(origin)
	startFakePlanStepKeeper(step, defaultPlanStepRenewEvery, clock)

	timers := clock.waitForTimerCount(t, 2)
	if timers[0].duration != 30*time.Second || timers[1].duration != time.Minute {
		t.Fatalf("initial renewal timers were not derived from the granted lease: renew=%s expiry=%s", timers[0].duration, timers[1].duration)
	}
	clock.fire(t, 0)
	timers = clock.waitForTimerCount(t, 4)
	if timers[2].duration != 10*time.Second || timers[3].duration != 40*time.Second {
		t.Fatalf("renewal timers were not recalculated from the renewed expiry: renew=%s expiry=%s", timers[2].duration, timers[3].duration)
	}
	step.cancel()
	<-step.done
}

func TestStepLeaseKeeperRejectsMalformedOrStaleRenewal(t *testing.T) {
	for _, test := range []struct {
		name     string
		expiryFn func(time.Time) time.Time
	}{
		{name: "zero expiry", expiryFn: func(time.Time) time.Time { return time.Time{} }},
		{name: "non-advancing expiry", expiryFn: func(current time.Time) time.Time { return current }},
		{name: "expired expiry", expiryFn: func(current time.Time) time.Time { return current.Add(-time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin := time.Now().UTC()
			expiry := origin.Add(time.Minute)
			steps, _ := planStepTestFixture()
			callback := newPlanStepTestCallback(steps)
			callback.claimResponseMutation = func(claim PlanStepClaim) PlanStepClaim {
				claim.LeaseExpiresAt = expiry
				return claim
			}
			worker := planStepTestWorker(callback)
			step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
			if err != nil || !available {
				t.Fatalf("claim failed: available=%v err=%v", available, err)
			}
			stopPlanStepKeeperForTest(t, step)
			callback.renewResponses = []PlanStepLease{{LeaseExpiresAt: test.expiryFn(expiry)}}
			clock := newFakePlanStepClock(origin)
			startFakePlanStepKeeper(step, defaultPlanStepRenewEvery, clock)
			clock.waitForTimerCount(t, 2)
			clock.fire(t, 0)

			select {
			case <-step.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("invalid lease renewal did not cancel active work")
			}
			<-step.done
			if !errors.Is(step.Lost(), ErrPlanStepExecutionLost) {
				t.Fatalf("invalid renewal was not terminal: %v", step.Lost())
			}
		})
	}
}

func TestStepLeaseExpiryCancelsWorkWhileRenewalIsBlocked(t *testing.T) {
	origin := time.Now().UTC()
	expiry := origin.Add(time.Minute)
	steps, _ := planStepTestFixture()
	callback := newPlanStepTestCallback(steps)
	callback.claimResponseMutation = func(claim PlanStepClaim) PlanStepClaim {
		claim.LeaseExpiresAt = expiry
		return claim
	}
	callback.renewBlock = make(chan struct{})
	callback.renewStarted = make(chan struct{}, 1)
	worker := planStepTestWorker(callback)
	step, available, err := claimNextPlanStepExecution(context.Background(), worker, uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String(), steps)
	if err != nil || !available {
		t.Fatalf("claim failed: available=%v err=%v", available, err)
	}
	stopPlanStepKeeperForTest(t, step)
	clock := newFakePlanStepClock(origin)
	startFakePlanStepKeeper(step, defaultPlanStepRenewEvery, clock)
	clock.waitForTimerCount(t, 2)
	clock.fire(t, 0)
	select {
	case <-callback.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("lease renewal did not start")
	}
	clock.fire(t, 1)
	select {
	case <-step.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("step expiry did not cancel the in-flight renewal and work context")
	}
	<-step.done
	if !errors.Is(step.Lost(), ErrPlanStepExecutionLost) {
		t.Fatalf("expiry was not made terminal: %v", step.Lost())
	}
}
