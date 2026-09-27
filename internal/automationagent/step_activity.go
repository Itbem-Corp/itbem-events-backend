package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"events-stocks/models"
	"github.com/gofrs/uuid"
)

const (
	maxPlanStepActivityAttempts = 3
	maxPlanStepActivityBody     = 8 << 10
)

var ErrPlanStepActivityUnavailable = errors.New("automation plan-step activity callback is unavailable")
var errActivityReportedNonzeroExit = errors.New("activity command returned a non-zero exit code")

// PlanStepActivityRequest is intentionally a metadata-only projection. Content,
// prompts, commands, paths, arguments, and tool results have no fields here.
// StepID is supplied in the URL and is never serialized into the request body.
type PlanStepActivityRequest struct {
	EventID      string                                  `json:"event_id"`
	TaskID       string                                  `json:"task_id"`
	RunID        string                                  `json:"run_id"`
	WorkerID     string                                  `json:"worker_id"`
	AgentKey     string                                  `json:"agent_key"`
	MachineID    string                                  `json:"machine_id,omitempty"`
	FencingToken string                                  `json:"fencing_token"`
	Sequence     uint64                                  `json:"sequence"`
	Action       string                                  `json:"action"`
	Phase        string                                  `json:"phase"`
	CallID       string                                  `json:"call_id,omitempty"`
	ReceiptID    string                                  `json:"receipt_id,omitempty"`
	ToolName     string                                  `json:"tool_name,omitempty"`
	DurationMS   int64                                   `json:"duration_ms,omitempty"`
	Details      *models.DeliveryPlanStepActivityDetails `json:"details,omitempty"`
}

// planStepActivityCallback is an optional extension to TaskCallback. Legacy
// callbacks remain valid; production's HTTPCallback implements this method.
type planStepActivityCallback interface {
	RecordPlanStepActivity(context.Context, string, PlanStepActivityRequest) error
}

type planStepActivityState struct {
	StepID   string                   `json:"step_id"`
	RunID    string                   `json:"run_id"`
	Sequence uint64                   `json:"sequence"`
	Pending  *planStepActivityPending `json:"pending,omitempty"`
}

type planStepActivityPending struct {
	StepID  string                  `json:"step_id"`
	Request PlanStepActivityRequest `json:"request"`
}

type stepActivityRecorder struct {
	callback   planStepActivityCallback
	step       *planStepExecution
	stepID     string
	lease      PlanStepLeaseRequest
	checkpoint *agentCheckpoint
	save       func() error
	now        func() time.Time
	mu         sync.Mutex
}

type stepActivityContextKey struct{}
type stepActivityActionContextKey struct{}
type stepActivitySuppressedContextKey struct{}

type stepActivityAction struct {
	Action   string
	ToolName string
}

func planStepActivityCallbackFor(callback TaskCallback) (planStepActivityCallback, bool) {
	switch value := callback.(type) {
	case identityTaskCallback:
		activity, ok := value.inner.(planStepActivityCallback)
		return activity, ok
	case *identityTaskCallback:
		if value == nil {
			return nil, false
		}
		activity, ok := value.inner.(planStepActivityCallback)
		return activity, ok
	default:
		activity, ok := callback.(planStepActivityCallback)
		return activity, ok
	}
}

func newStepActivityRecorder(callback TaskCallback, step *planStepExecution, checkpoint *agentCheckpoint, save func() error, now func() time.Time) (*stepActivityRecorder, error) {
	if step == nil || checkpoint == nil || save == nil || step.claim.Step == nil {
		return nil, fmt.Errorf("automation plan-step activity recorder is invalid")
	}
	activity, ok := planStepActivityCallbackFor(callback)
	if !ok {
		return nil, ErrPlanStepActivityUnavailable
	}
	if now == nil {
		now = time.Now
	}
	if checkpoint.StepActivities == nil {
		checkpoint.StepActivities = make(map[string]planStepActivityState)
	}
	return &stepActivityRecorder{
		callback: activity, step: step, stepID: step.lease.StepID, lease: step.lease,
		checkpoint: checkpoint, save: save, now: now,
	}, nil
}

func (recorder *stepActivityRecorder) checkActive(ctx context.Context) error {
	if recorder == nil || recorder.step == nil {
		if ctx != nil {
			return ctx.Err()
		}
		return nil
	}
	return recorder.step.checkActive(ctx)
}

func withStepActivityRecorder(ctx context.Context, recorder *stepActivityRecorder) context.Context {
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, stepActivityContextKey{}, recorder)
}

func stepActivityFromContext(ctx context.Context) *stepActivityRecorder {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(stepActivityContextKey{}).(*stepActivityRecorder)
	return value
}

func withStepActivityAction(ctx context.Context, action, toolName string) context.Context {
	return context.WithValue(ctx, stepActivityActionContextKey{}, stepActivityAction{Action: action, ToolName: toolName})
}

func withStepActivitySuppressed(ctx context.Context) context.Context {
	return context.WithValue(ctx, stepActivitySuppressedContextKey{}, true)
}

func stepActivityIsSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	suppressed, _ := ctx.Value(stepActivitySuppressedContextKey{}).(bool)
	return suppressed
}

// runStepActivity brackets one observable operation with persisted start and
// terminal events. The pending request is checkpointed before each POST; if a
// response is lost, recovery retries that exact payload and event ID.
func runStepActivity(ctx context.Context, action, toolName string, operation func() error) (operationErr error, reportErr error) {
	return runStepActivityWithDetails(ctx, action, toolName, nil, operation)
}

// runStepActivityWithDetails records bounded typed details only after the
// operation has reached a terminal phase. Start events remain detail-free.
func runStepActivityWithDetails(ctx context.Context, action, toolName string, details func(phase string) *models.DeliveryPlanStepActivityDetails, operation func() error) (operationErr error, reportErr error) {
	return runStepActivityWithInferenceReceipt(ctx, action, toolName, details, nil, operation)
}

// runStepActivityWithInferenceReceipt adds only opaque gateway identifiers to
// the terminal inference event. Provider metadata and accounting remain
// server-derived from the canonical receipt.
func runStepActivityWithInferenceReceipt(ctx context.Context, action, toolName string, details func(phase string) *models.DeliveryPlanStepActivityDetails, inferenceIDs func() (string, string), operation func() error) (operationErr error, reportErr error) {
	recorder := stepActivityFromContext(ctx)
	if recorder != nil {
		if err := recorder.checkActive(ctx); err != nil {
			return nil, err
		}
	} else if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if recorder == nil || stepActivityIsSuppressed(ctx) {
		operationErr = operation()
		if ctx != nil && ctx.Err() != nil {
			return operationErr, ctx.Err()
		}
		return operationErr, nil
	}
	startedAt := recorder.now()
	if err := recorder.record(ctx, action, "started", toolName, 0, nil, "", ""); err != nil {
		return nil, err
	}
	if err := recorder.checkActive(ctx); err != nil {
		return nil, err
	}
	operationErr = operation()
	if errors.Is(operationErr, ErrPlanStepLeaseLost) {
		recorder.step.lose(ErrPlanStepExecutionLost)
	}
	if err := recorder.checkActive(ctx); err != nil {
		return operationErr, err
	}
	phase := "completed"
	if operationErr != nil {
		phase = "failed"
	}
	duration := recorder.now().Sub(startedAt).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	var terminalDetails *models.DeliveryPlanStepActivityDetails
	if details != nil {
		terminalDetails = details(phase)
	}
	callID, receiptID := "", ""
	if action == models.DeliveryPlanStepActivityInference && inferenceIDs != nil {
		callID, receiptID = inferenceIDs()
	}
	if err := recorder.record(ctx, action, phase, toolName, duration, terminalDetails, callID, receiptID); err != nil {
		return operationErr, err
	}
	return operationErr, nil
}

func (recorder *stepActivityRecorder) record(ctx context.Context, action, phase, toolName string, durationMS int64, details *models.DeliveryPlanStepActivityDetails, callID, receiptID string) error {
	if recorder == nil {
		return nil
	}
	if err := recorder.checkActive(ctx); err != nil {
		return err
	}
	request := PlanStepActivityRequest{
		TaskID: recorder.lease.TaskID, RunID: recorder.lease.RunID,
		WorkerID: recorder.lease.WorkerID, AgentKey: recorder.lease.AgentKey,
		MachineID: recorder.lease.MachineID, FencingToken: recorder.lease.FencingToken,
		Action: action, Phase: phase, CallID: callID, ReceiptID: receiptID, ToolName: toolName, DurationMS: durationMS, Details: details,
	}
	if request.Action != "tool" {
		request.ToolName = ""
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := recorder.checkActive(ctx); err != nil {
		return err
	}
	if recorder.checkpoint.StepActivities == nil {
		recorder.checkpoint.StepActivities = make(map[string]planStepActivityState)
	}
	stateKey := planStepActivityStateKey(recorder.lease.RunID, recorder.stepID)
	state := recorder.checkpoint.StepActivities[stateKey]
	if (state.StepID != "" && state.StepID != recorder.stepID) || (state.RunID != "" && state.RunID != recorder.lease.RunID) {
		return fmt.Errorf("plan-step activity checkpoint is bound to another execution")
	}
	state.StepID = recorder.stepID
	state.RunID = recorder.lease.RunID
	if state.Pending != nil {
		return fmt.Errorf("a prior plan-step activity is still pending reconciliation")
	}
	request.Sequence = state.Sequence + 1
	eventID, err := uuid.NewV4()
	if err != nil {
		return fmt.Errorf("plan-step activity ID could not be generated")
	}
	request.EventID = eventID.String()
	if err := validatePlanStepActivityRequest(recorder.stepID, request); err != nil {
		return err
	}
	if err := recorder.checkActive(ctx); err != nil {
		return err
	}
	state.Pending = &planStepActivityPending{StepID: recorder.stepID, Request: request}
	recorder.checkpoint.StepActivities[stateKey] = state
	if err := recorder.save(); err != nil {
		return fmt.Errorf("plan-step activity checkpoint could not be saved")
	}
	if err := recorder.checkActive(ctx); err != nil {
		return err
	}
	if err := recorder.callback.RecordPlanStepActivity(ctx, recorder.stepID, request); err != nil {
		if errors.Is(err, ErrPlanStepLeaseLost) && recorder.step != nil {
			recorder.step.lose(ErrPlanStepExecutionLost)
			return ErrPlanStepExecutionLost
		}
		return err
	}
	if err := recorder.checkActive(ctx); err != nil {
		return err
	}
	state.Sequence = request.Sequence
	state.Pending = nil
	recorder.checkpoint.StepActivities[stateKey] = state
	if err := recorder.checkActive(ctx); err != nil {
		return err
	}
	if err := recorder.save(); err != nil {
		// The durable checkpoint still carries the identical pending event. A
		// retry is safe because the server deduplicates by event_id.
		return fmt.Errorf("plan-step activity checkpoint could not be finalized")
	}
	return recorder.checkActive(ctx)
}

func retryPendingPlanStepActivities(ctx context.Context, callback TaskCallback, checkpoint *agentCheckpoint, save func() error) error {
	if checkpoint == nil || len(checkpoint.StepActivities) == 0 {
		return nil
	}
	activity, available := planStepActivityCallbackFor(callback)
	if !available {
		for _, state := range checkpoint.StepActivities {
			if state.Pending != nil {
				return ErrPlanStepActivityUnavailable
			}
		}
		return nil
	}
	stateKeys := make([]string, 0, len(checkpoint.StepActivities))
	for stateKey, state := range checkpoint.StepActivities {
		if state.Pending != nil {
			stateKeys = append(stateKeys, stateKey)
		}
	}
	sort.Strings(stateKeys)
	for _, stateKey := range stateKeys {
		state := checkpoint.StepActivities[stateKey]
		pending := state.Pending
		stepID := state.StepID
		if pending == nil || !validStepCallbackUUID(stepID) || pending.StepID != stepID || !validStepCallbackUUID(state.RunID) || stateKey != planStepActivityStateKey(state.RunID, stepID) || pending.Request.RunID != state.RunID {
			return fmt.Errorf("plan-step activity checkpoint is invalid")
		}
		if err := validatePlanStepActivityRequest(stepID, pending.Request); err != nil {
			return fmt.Errorf("plan-step activity checkpoint is invalid")
		}
		if pending.Request.Sequence != state.Sequence+1 {
			return fmt.Errorf("plan-step activity sequence is inconsistent")
		}
		if err := activity.RecordPlanStepActivity(ctx, stepID, pending.Request); err != nil {
			return err
		}
		state.Sequence = pending.Request.Sequence
		state.Pending = nil
		checkpoint.StepActivities[stateKey] = state
		if err := save(); err != nil {
			return fmt.Errorf("plan-step activity checkpoint could not be finalized")
		}
	}
	return nil
}

func planStepActivityStateKey(runID, stepID string) string {
	return runID + "/" + stepID
}

func validatePlanStepActivityRequest(stepID string, request PlanStepActivityRequest) error {
	if !validStepCallbackUUID(stepID) || !validStepCallbackUUID(request.EventID) || !validStepCallbackUUID(request.TaskID) || !validStepCallbackUUID(request.RunID) || !validStepCallbackUUID(request.WorkerID) || !validFencingToken(request.FencingToken) || request.Sequence == 0 {
		return fmt.Errorf("automation step activity identity is invalid")
	}
	if !validPlanStepWorkerIdentity(request.AgentKey, request.MachineID) {
		return fmt.Errorf("automation step activity worker profile is invalid")
	}
	switch request.Action {
	case "inference", "tool", "file_read", "file_change", "command", "validation", "evidence":
	default:
		return fmt.Errorf("automation step activity action is invalid")
	}
	switch request.Phase {
	case "started", "completed", "failed":
	default:
		return fmt.Errorf("automation step activity phase is invalid")
	}
	if request.DurationMS < 0 || (request.Phase == "started" && request.DurationMS != 0) {
		return fmt.Errorf("automation step activity duration is invalid")
	}
	callID, receiptID := strings.TrimSpace(request.CallID), strings.TrimSpace(request.ReceiptID)
	if (callID == "") != (receiptID == "") {
		return fmt.Errorf("automation step activity inference receipt identifiers are incomplete")
	}
	if callID != "" {
		parsedCallID, callErr := uuid.FromString(callID)
		parsedReceiptID, receiptErr := uuid.FromString(receiptID)
		if callID != request.CallID || receiptID != request.ReceiptID || request.Action != "inference" || request.Phase == "started" || callErr != nil || receiptErr != nil || parsedCallID == uuid.Nil || parsedReceiptID == uuid.Nil || parsedCallID.String() != callID || parsedReceiptID.String() != receiptID {
			return fmt.Errorf("automation step activity inference receipt identifiers are invalid")
		}
	}
	if request.Action == "inference" && request.Phase == "completed" && callID == "" {
		return fmt.Errorf("completed inference activity requires a durable receipt")
	}
	if strings.TrimSpace(request.ToolName) != "" && !validPlanStepActivityTool(request.Action, request.ToolName) {
		return fmt.Errorf("automation step activity tool is invalid")
	}
	if err := models.ValidateDeliveryPlanStepActivityDetails(request.Action, request.Phase, request.Details); err != nil {
		return fmt.Errorf("automation step activity details are invalid")
	}
	return nil
}

func validPlanStepActivityTool(action, tool string) bool {
	return action == "tool" && tool == "agent_action"
}

func (c *HTTPCallback) RecordPlanStepActivity(ctx context.Context, stepID string, request PlanStepActivityRequest) error {
	if err := validatePlanStepActivityRequest(stepID, request); err != nil {
		return err
	}
	if c == nil || c.client == nil || c.baseURL == "" || len(c.identity.privateKey) == 0 || c.instanceID == "" {
		return ErrPlanStepActivityUnavailable
	}
	body, err := json.Marshal(request)
	if err != nil || len(body) == 0 || len(body) > maxPlanStepActivityBody {
		return fmt.Errorf("automation step activity request exceeds the allowed size")
	}
	var lastErr error
	for attempt := 0; attempt < maxPlanStepActivityAttempts; attempt++ {
		requestContext, cancel := context.WithTimeout(ctx, planStepCallbackTimeout)
		req, requestErr := c.newSignedRequest(requestContext, http.MethodPost, c.baseURL+"/api/internal/automation/steps/"+stepID+"/activity", body)
		if requestErr != nil {
			cancel()
			return requestErr
		}
		req.Header.Set("Content-Type", "application/json")
		response, requestErr := c.client.Do(req)
		if requestErr != nil {
			cancel()
			if ctx.Err() != nil {
				return fmt.Errorf("automation step activity request was cancelled")
			}
			lastErr = fmt.Errorf("automation step activity request failed")
			if !waitPlanStepActivityRetry(ctx, attempt) {
				return lastErr
			}
			continue
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxPlanStepCallbackResponseBytes+1))
		_ = response.Body.Close()
		cancel()
		if readErr != nil || len(responseBody) > maxPlanStepCallbackResponseBytes {
			lastErr = fmt.Errorf("automation step activity response could not be read")
			if attempt+1 < maxPlanStepActivityAttempts && waitPlanStepActivityRetry(ctx, attempt) {
				continue
			}
			return lastErr
		}
		if response.StatusCode == http.StatusConflict {
			return ErrPlanStepLeaseLost
		}
		if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooEarly || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
			lastErr = fmt.Errorf("automation step activity request failed (%d)", response.StatusCode)
			if attempt+1 < maxPlanStepActivityAttempts && waitPlanStepActivityRetry(ctx, attempt) {
				continue
			}
			return lastErr
		}
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
			return fmt.Errorf("automation step activity rejected (%d)", response.StatusCode)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(responseBody, &envelope) != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return fmt.Errorf("automation step activity response is invalid")
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("automation step activity request failed")
}

func waitPlanStepActivityRetry(ctx context.Context, attempt int) bool {
	delay := time.Duration(attempt+1) * 50 * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func activityActionFromContext(ctx context.Context, fallbackAction, fallbackTool string) (string, string) {
	if ctx != nil {
		if action, ok := ctx.Value(stepActivityActionContextKey{}).(stepActivityAction); ok && action.Action != "" {
			return action.Action, action.ToolName
		}
	}
	return fallbackAction, fallbackTool
}
