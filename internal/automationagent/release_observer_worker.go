package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (w *Worker) processReleaseObservation(ctx context.Context, taskID, runID string, input TaskInput) error {
	if w.releaseObserver == nil {
		return w.fail(ctx, taskID, runID, fmt.Errorf("signed release observer unavailable"))
	}
	accepted, err := w.callback.Update(ctx, taskID, TaskUpdate{Status: "running", RunID: runID, ProgressStep: "validating"})
	if err != nil {
		return err
	}
	if !accepted {
		return nil
	}
	// Persist the immutable observation input before contacting the server.
	// Queue leases and signing credentials are deliberately excluded.
	requestBody, err := json.Marshal(map[string]any{"schema_version": 1, "task_id": taskID, "run_id": runID, "operation": "delivery.release_gate", "deterministic": true, "delivery": input.Delivery, "execution_identity": w.identity()})
	if err != nil {
		return w.fail(ctx, taskID, runID, err)
	}
	requestKey := "automation/" + taskID + "/runs/" + runID + "/request.json"
	if err := w.store.PutEncryptedJSON(ctx, w.config.OutputBucket, requestKey, requestBody); err != nil {
		return &RetryableError{Message: "release observation request storage unavailable", RetryAfter: time.Minute}
	}
	requestRef := "s3://" + w.config.OutputBucket + "/" + requestKey
	handoff, err := w.releaseObserver(ctx, taskID, runID, input.Delivery)
	if err != nil {
		var retryable *RetryableError
		if errors.As(err, &retryable) {
			return retryable
		}
		var gatewayError *gatewayRequestError
		if errors.As(err, &gatewayError) && gatewayResponseIsTransient(gatewayError.statusCode) {
			return &RetryableError{Message: "release observation temporarily unavailable", RetryAfter: time.Minute}
		}
		return w.fail(ctx, taskID, runID, err)
	}
	raw, err := json.Marshal(handoff)
	if err != nil {
		return w.fail(ctx, taskID, runID, err)
	}
	handoff, err = validateReleaseObservation(taskID, input.Delivery, raw)
	if err != nil {
		return w.fail(ctx, taskID, runID, err)
	}
	accepted, err = w.callback.Update(ctx, taskID, TaskUpdate{Status: "running", RunID: runID, ProgressStep: "validating"})
	if err != nil {
		return err
	}
	if !accepted {
		return nil
	}
	output := map[string]any{"schema_version": 1, "task_id": taskID, "run_id": runID, "request_ref": requestRef, "operation": "delivery.release_gate", "deterministic": true, "execution": handoff, "execution_identity": w.identity(), "created_at": w.now().UTC().Format(time.RFC3339Nano)}
	encoded, err := json.Marshal(output)
	if err != nil {
		return w.fail(ctx, taskID, runID, err)
	}
	ref, err := w.storeExecutionResult(ctx, taskID, runID, encoded)
	if err != nil {
		return err
	}
	_, err = w.callback.Update(ctx, taskID, TaskUpdate{Status: "completed", RunID: runID, RequestRef: requestRef, OutputRef: ref, Execution: handoff, Deterministic: true})
	return err
}
