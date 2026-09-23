package automationagent

import (
	"context"
	"errors"
	"testing"
)

func TestWorkerCapabilitiesAreAllowlistedAndFailClosed(t *testing.T) {
	got, err := parseWorkerCapabilities("delivery.plan, delivery.qa")
	if err != nil || len(got) != 2 || got[0] != "delivery.plan" || got[1] != "delivery.qa" {
		t.Fatalf("capabilities = %#v, err = %v", got, err)
	}
	for _, raw := range []string{"delivery.plan,delivery.plan", "delivery.unknown", ",delivery.plan"} {
		if _, err := parseWorkerCapabilities(raw); err == nil {
			t.Fatalf("accepted invalid capability list %q", raw)
		}
	}
}

func TestWorkerDoesNotClaimUnsupportedOperation(t *testing.T) {
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{
		InputBucket:       "itbem-ai-inputs-local",
		OutputBucket:      "itbem-ai-outputs-local",
		AllowedOperations: []string{"delivery.plan"},
	}, &fakeStore{}, callback, fakeProvider{})
	if err != nil {
		t.Fatal(err)
	}
	message := TaskMessage{SchemaVersion: 1, JobID: "job", TenantCode: "itbem", Type: "ai.local.process"}
	message.Payload.TaskID = "task"
	message.Payload.Operation = "delivery.qa"
	message.Payload.Attempt = 1
	message.Payload.InputRef = "s3://itbem-ai-inputs-local/automation/inputs/task/input.json"
	err = worker.Process(context.Background(), message)
	var retryable *RetryableError
	if !errors.As(err, &retryable) || retryable.RetryAfter <= 0 {
		t.Fatalf("unsupported operation should remain retryable, got %v", err)
	}
	if len(callback.updates) != 0 {
		t.Fatal("unsupported operation claimed a task before checking capabilities")
	}
}
