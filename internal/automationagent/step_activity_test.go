package automationagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"events-stocks/models"
)

type activityCollector struct {
	*fakeCallback
	requests   []PlanStepActivityRequest
	stepIDs    []string
	checkpoint *agentCheckpoint
}

func (callback *activityCollector) RecordPlanStepActivity(_ context.Context, stepID string, request PlanStepActivityRequest) error {
	callback.stepIDs = append(callback.stepIDs, stepID)
	callback.requests = append(callback.requests, request)
	if callback.checkpoint != nil {
		state := callback.checkpoint.StepActivities[planStepActivityStateKey(request.RunID, stepID)]
		if state.Pending == nil || state.Pending.Request.EventID != request.EventID {
			return errors.New("activity was sent before the pending checkpoint was saved")
		}
	}
	return nil
}

func validActivityRequest() PlanStepActivityRequest {
	return PlanStepActivityRequest{
		EventID: stepCallbackUUID(), TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(),
		WorkerID: stepCallbackUUID(), AgentKey: "generalist", MachineID: stepCallbackUUID(),
		FencingToken: "fence-activity-test", Sequence: 1,
		Action: "tool", Phase: "started", ToolName: "agent_action",
	}
}

func TestStepActivityRecorderPersistsMonotonicSequenceAndOrdersEvents(t *testing.T) {
	stepID := stepCallbackUUID()
	lease := PlanStepLeaseRequest{
		StepID: stepID, TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "fence-activity-test",
	}
	manager := &planStepExecution{
		claim: PlanStepClaim{Step: &PlanStepDTO{ID: stepID}}, lease: lease,
	}
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	collector := &activityCollector{fakeCallback: &fakeCallback{}, checkpoint: checkpoint}
	var persisted []agentCheckpoint
	save := func() error {
		body, err := json.Marshal(checkpoint)
		if err != nil {
			return err
		}
		var snapshot agentCheckpoint
		if err := json.Unmarshal(body, &snapshot); err != nil {
			return err
		}
		persisted = append(persisted, snapshot)
		return nil
	}
	recorder, err := newStepActivityRecorder(collector, manager, checkpoint, save, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	// Wrap the callback's append so the test can prove the start was durable and
	// observed before the action body, and the terminal followed it.
	wrapped := &orderedActivityCallback{activityCollector: collector, order: &order}
	recorder.callback = wrapped
	operationErr, reportErr := runStepActivity(withStepActivityRecorder(context.Background(), recorder), "tool", "agent_action", func() error {
		if len(collector.requests) != 1 || collector.requests[0].Phase != "started" {
			t.Fatal("action ran before the start event was accepted")
		}
		order = append(order, "action")
		return nil
	})
	if operationErr != nil || reportErr != nil {
		t.Fatalf("activity run: operation=%v report=%v", operationErr, reportErr)
	}
	if got, want := order, []string{"event:started", "action", "event:completed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("event order = %#v, want %#v", got, want)
	}
	if len(collector.requests) != 2 || len(persisted) != 4 {
		t.Fatalf("requests=%d checkpoint saves=%d, want 2 and 4", len(collector.requests), len(persisted))
	}
	for index, request := range collector.requests {
		if request.Sequence != uint64(index+1) || request.TaskID != lease.TaskID || request.RunID != lease.RunID || request.WorkerID != lease.WorkerID || request.AgentKey != lease.AgentKey || request.MachineID != lease.MachineID || request.FencingToken != lease.FencingToken {
			t.Fatalf("event identity/sequence mismatch: %#v", request)
		}
	}
	state := checkpoint.StepActivities[planStepActivityStateKey(lease.RunID, stepID)]
	if state.Sequence != 2 || state.Pending != nil {
		t.Fatalf("checkpoint did not finalize sequence 2: %#v", state)
	}
}

func TestInferenceActivityCarriesOnlyOpaqueReceiptIDsOnTerminalEvent(t *testing.T) {
	stepID := stepCallbackUUID()
	lease := PlanStepLeaseRequest{
		StepID: stepID, TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "fence-inference-receipt",
	}
	manager := &planStepExecution{claim: PlanStepClaim{Step: &PlanStepDTO{ID: stepID}}, lease: lease}
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	collector := &activityCollector{fakeCallback: &fakeCallback{}, checkpoint: checkpoint}
	recorder, err := newStepActivityRecorder(collector, manager, checkpoint, func() error { return nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	callID, receiptID := stepCallbackUUID(), stepCallbackUUID()
	operationErr, reportErr := runStepActivityWithInferenceReceipt(
		withStepActivityRecorder(context.Background(), recorder), models.DeliveryPlanStepActivityInference,
		"provider_inference", nil, func() (string, string) { return callID, receiptID }, func() error { return nil },
	)
	if operationErr != nil || reportErr != nil || len(collector.requests) != 2 {
		t.Fatalf("inference activity failed: operation=%v report=%v requests=%#v", operationErr, reportErr, collector.requests)
	}
	started, terminal := collector.requests[0], collector.requests[1]
	if started.Phase != models.DeliveryPlanStepActivityStarted || started.CallID != "" || started.ReceiptID != "" {
		t.Fatalf("start event must not carry a receipt: %#v", started)
	}
	if terminal.Phase != models.DeliveryPlanStepActivityCompleted || terminal.CallID != callID || terminal.ReceiptID != receiptID {
		t.Fatalf("terminal inference event did not carry opaque gateway IDs: %#v", terminal)
	}
	encoded, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"provider", "model", "usage", "cost", "prompt", "content", "reasoning"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("worker activity body exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestStepActivitySequenceRestartsForANewRunOnTheSameStep(t *testing.T) {
	stepID := stepCallbackUUID()
	priorRunID := stepCallbackUUID()
	currentRunID := stepCallbackUUID()
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{
		planStepActivityStateKey(priorRunID, stepID): {StepID: stepID, RunID: priorRunID, Sequence: 22},
	}}
	lease := PlanStepLeaseRequest{
		StepID: stepID, TaskID: stepCallbackUUID(), RunID: currentRunID, WorkerID: stepCallbackUUID(),
		AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "fence-new-run",
	}
	manager := &planStepExecution{claim: PlanStepClaim{Step: &PlanStepDTO{ID: stepID}}, lease: lease}
	collector := &activityCollector{fakeCallback: &fakeCallback{}, checkpoint: checkpoint}
	recorder, err := newStepActivityRecorder(collector, manager, checkpoint, func() error { return nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, reportErr := runStepActivity(withStepActivityRecorder(context.Background(), recorder), "tool", "agent_action", func() error { return nil })
	if reportErr != nil {
		t.Fatal(reportErr)
	}
	if len(collector.requests) != 2 || collector.requests[0].RunID != currentRunID || collector.requests[0].Sequence != 1 || collector.requests[1].Sequence != 2 {
		t.Fatalf("new run did not start a fresh per-run sequence: %#v", collector.requests)
	}
	if checkpoint.StepActivities[planStepActivityStateKey(priorRunID, stepID)].Sequence != 22 || checkpoint.StepActivities[planStepActivityStateKey(currentRunID, stepID)].Sequence != 2 {
		t.Fatalf("checkpoint mixed step sequences across runs: %#v", checkpoint.StepActivities)
	}
}

type orderedActivityCallback struct {
	*activityCollector
	order *[]string
}

func (callback *orderedActivityCallback) RecordPlanStepActivity(ctx context.Context, stepID string, request PlanStepActivityRequest) error {
	if err := callback.activityCollector.RecordPlanStepActivity(ctx, stepID, request); err != nil {
		return err
	}
	*callback.order = append(*callback.order, "event:"+request.Phase)
	return nil
}

func TestPendingStepActivityRecoveryReusesExactEventIdentity(t *testing.T) {
	stepID := stepCallbackUUID()
	request := validActivityRequest()
	request.EventID = stepCallbackUUID()
	request.Sequence = 18
	request.TaskID = stepCallbackUUID()
	stateKey := planStepActivityStateKey(request.RunID, stepID)
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{
		stateKey: {StepID: stepID, RunID: request.RunID, Sequence: 17, Pending: &planStepActivityPending{StepID: stepID, Request: request}},
	}}
	checkpoint.TaskID = request.TaskID
	collector := &activityCollector{fakeCallback: &fakeCallback{}}
	saves := 0
	err := retryPendingPlanStepActivities(context.Background(), collector, checkpoint, func() error { saves++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(collector.requests) != 1 || collector.requests[0].EventID != request.EventID || collector.requests[0].Sequence != 18 || collector.stepIDs[0] != stepID {
		t.Fatalf("pending event was not replayed identically: %#v steps=%v", collector.requests, collector.stepIDs)
	}
	state := checkpoint.StepActivities[stateKey]
	if state.Sequence != 18 || state.Pending != nil || saves != 1 {
		t.Fatalf("recovery checkpoint state = %#v, saves=%d", state, saves)
	}
}

func TestPlanStepActivityCallbackRetriesTheSamePayloadAfterUncertainResponse(t *testing.T) {
	const privateMarker = "PRIVATE_ACTIVITY_RESPONSE_MUST_NOT_LEAK"
	stepID := stepCallbackUUID()
	identity, instanceID := newTestMachineIdentity(t)
	var bodies [][]byte
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost || request.URL.Path != "/api/internal/automation/steps/"+stepID+"/activity" {
			t.Errorf("unexpected activity endpoint: %s", request.URL.Path)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error("could not read callback payload")
		}
		assertSignedCallbackRequest(t, request, body, identity, instanceID)
		bodies = append(bodies, body)
		if calls == 1 {
			// Simulate an uncertain outcome: the remote service may have committed
			// the event, but the client receives a retryable response.
			http.Error(w, privateMarker, http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"accepted":true}}`)
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instanceID)
	request := validActivityRequest()
	request.Action, request.Phase = "tool", "completed"
	request.ToolName = "agent_action"
	request.DurationMS = 37
	if err := callback.RecordPlanStepActivity(context.Background(), stepID, request); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry changed payload: calls=%d bodies=%q", calls, bodies)
	}
	var payload map[string]any
	if err := json.Unmarshal(bodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"action", "agent_key", "duration_ms", "event_id", "fencing_token", "machine_id", "phase", "run_id", "sequence", "task_id", "tool_name", "worker_id"}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	// JSON object key order is not semantically significant.
	sort.Strings(keys)
	sort.Strings(wantKeys)
	if !reflect.DeepEqual(keys, wantKeys) || strings.Contains(string(bodies[0]), privateMarker) || strings.Contains(string(bodies[0]), "callback-master-test-value") {
		t.Fatalf("activity body is not metadata-only: keys=%v body=%s", keys, bodies[0])
	}
}

func TestPlanStepActivityCallbackRedactsErrorBodiesAndValidatesActionAllowlist(t *testing.T) {
	const privateMarker = "PRIVATE_ACTIVITY_RESPONSE_MUST_NOT_LEAK"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, privateMarker, http.StatusBadRequest)
	}))
	defer server.Close()
	callback := newStepCallbackForTest(t, server)
	request := validActivityRequest()
	request.Action = "file_read"
	request.ToolName = "workspace_read"
	if err := callback.RecordPlanStepActivity(context.Background(), stepCallbackUUID(), request); err == nil || strings.Contains(err.Error(), privateMarker) || strings.Contains(err.Error(), "callback-master-test-value") {
		t.Fatalf("private response body leaked through callback error: %v", err)
	}
	request.Action = "tool\nPRIVATE"
	if err := callback.RecordPlanStepActivity(context.Background(), stepCallbackUUID(), request); err == nil || !strings.Contains(err.Error(), "action is invalid") {
		t.Fatalf("non-allowlisted action was accepted: %v", err)
	}
}

func TestMissingActivityCallbackFailsClosedForPlanStepsButLegacyNoPlanActionRuns(t *testing.T) {
	stepID := stepCallbackUUID()
	manager := &planStepExecution{claim: PlanStepClaim{Step: &PlanStepDTO{ID: stepID}}}
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	_, err := newStepActivityRecorder(&fakeCallback{}, manager, checkpoint, func() error { return nil }, time.Now)
	if !errors.Is(err, ErrPlanStepActivityUnavailable) {
		t.Fatalf("plan-step callback absence did not fail closed: %v", err)
	}
	ran := false
	operationErr, reportErr := runStepActivity(context.Background(), "tool", "agent_action", func() error { ran = true; return nil })
	if !ran || operationErr != nil || reportErr != nil {
		t.Fatalf("legacy operation without a plan-step recorder changed behavior: ran=%v operation=%v report=%v", ran, operationErr, reportErr)
	}
}

func TestStepActivityCheckpointRejectsSensitiveOrMalformedState(t *testing.T) {
	stepID := stepCallbackUUID()
	step := PlanStepDTO{ID: stepID, StepKey: "work"}
	runID := stepCallbackUUID()
	stateKey := planStepActivityStateKey(runID, stepID)
	state := planStepActivityState{StepID: stepID, RunID: runID, Sequence: 1}
	if !validCheckpointPlanProgress(agentCheckpoint{StepActivities: map[string]planStepActivityState{stateKey: state}}, []PlanStepDTO{step}) {
		t.Fatal("valid activity sequence was rejected")
	}
	state.Pending = &planStepActivityPending{StepID: stepID, Request: validActivityRequest()}
	if validCheckpointPlanProgress(agentCheckpoint{TaskID: state.Pending.Request.TaskID, StepActivities: map[string]planStepActivityState{stateKey: state}}, []PlanStepDTO{step}) {
		t.Fatal("pending event sequence gap was accepted")
	}
	encoded, err := json.Marshal(validActivityRequest())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "prompt") || strings.Contains(string(encoded), "content") || strings.Contains(string(encoded), "arguments") || strings.Contains(string(encoded), "command") || strings.Contains(string(encoded), "path") {
		t.Fatalf("activity DTO exposes content-bearing fields: %s", encoded)
	}
}

func TestStepActivityDetailsAreTerminalTypedAndBounded(t *testing.T) {
	stepID := stepCallbackUUID()
	lease := PlanStepLeaseRequest{StepID: stepID, TaskID: stepCallbackUUID(), RunID: stepCallbackUUID(), WorkerID: stepCallbackUUID(), AgentKey: "generalist", MachineID: stepCallbackUUID(), FencingToken: "fence-details-test"}
	manager := &planStepExecution{claim: PlanStepClaim{Step: &PlanStepDTO{ID: stepID}}, lease: lease}
	checkpoint := &agentCheckpoint{StepActivities: map[string]planStepActivityState{}}
	collector := &activityCollector{fakeCallback: &fakeCallback{}}
	recorder, err := newStepActivityRecorder(collector, manager, checkpoint, func() error { return nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	details := &models.DeliveryPlanStepActivityDetails{ResourceReferences: []string{"workspace://backend"}, ChangedFiles: []string{"internal/orders.go"}}
	operationErr, reportErr := runStepActivityWithDetails(withStepActivityRecorder(context.Background(), recorder), models.DeliveryPlanStepActivityFileChange, "workspace_patch", func(string) *models.DeliveryPlanStepActivityDetails { return details }, func() error { return nil })
	if operationErr != nil || reportErr != nil || len(collector.requests) != 2 {
		t.Fatalf("terminal-detail activity failed: operation=%v report=%v requests=%#v", operationErr, reportErr, collector.requests)
	}
	if collector.requests[0].Details != nil || collector.requests[0].Phase != models.DeliveryPlanStepActivityStarted {
		t.Fatalf("start event unexpectedly included details: %#v", collector.requests[0])
	}
	if collector.requests[1].Details == nil || collector.requests[1].Details.ChangedFiles[0] != "internal/orders.go" || collector.requests[1].Phase != models.DeliveryPlanStepActivityCompleted {
		t.Fatalf("safe terminal detail was not preserved: %#v", collector.requests[1])
	}
	if err := validatePlanStepActivityRequest(stepID, collector.requests[1]); err != nil {
		t.Fatalf("safe terminal detail failed request validation: %v", err)
	}
	for _, unsafe := range []*models.DeliveryPlanStepActivityDetails{
		{ResourceReferences: []string{"workspace://backend/../../.env"}},
		{ChangedFiles: []string{"config/api_token.json"}},
	} {
		request := collector.requests[1]
		request.Details = unsafe
		if err := validatePlanStepActivityRequest(stepID, request); err == nil {
			t.Fatalf("unsafe activity path was accepted: %#v", unsafe)
		}
	}
}

func TestCommandActivityDetailsExcludeCommandsArgumentsAndOutput(t *testing.T) {
	const privateMarker = "PRIVATE_COMMAND_OUTPUT_MUST_NEVER_BE_SERIALIZED"
	result := commandResult{ExitCode: 0, Output: privateMarker}
	details := commandActivityDetails(models.DeliveryPlanStepActivityCommand, models.DeliveryPlanStepActivityCompleted, "C:\\Program Files\\Go\\go.exe", []string{"test", "./..."}, result, "workspace://backend")
	if details == nil || details.ExecutableName != "go" || details.ArgumentCount == nil || *details.ArgumentCount != 2 || details.ExitCode == nil || *details.ExitCode != 0 || details.CapturedOutputBytes == nil || *details.CapturedOutputBytes != len(privateMarker) {
		t.Fatalf("safe command summary was not captured: %#v", details)
	}
	encoded, err := json.Marshal(details)
	if err != nil || strings.Contains(string(encoded), privateMarker) || strings.Contains(string(encoded), "Program Files") || strings.Contains(string(encoded), "./...") || strings.Contains(string(encoded), "command") {
		t.Fatalf("command detail projection leaked private command data: %s (%v)", encoded, err)
	}
	if commandActivityDetails(models.DeliveryPlanStepActivityCommand, models.DeliveryPlanStepActivityCompleted, "sh", []string{"-c", "echo secret"}, result, "") != nil {
		t.Fatal("unsupported executable was included in activity details")
	}
}
