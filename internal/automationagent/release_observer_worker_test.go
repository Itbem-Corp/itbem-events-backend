package automationagent

import (
	"context"
	"encoding/json"
	"events-stocks/internal/agentwork"
	"testing"
)

func TestReleaseWorkerRefusesMissingSignedObserverWithoutInference(t *testing.T) {
	input, err := json.Marshal(TaskInput{Prompt: "synthetic release", Delivery: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}
	callback := &fakeCallback{operation: "delivery.release_gate"}
	provider := &countingProvider{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	message := validMessage()
	message.Payload.Operation = "delivery.release_gate"
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 || len(callback.updates) == 0 || callback.updates[len(callback.updates)-1].Status != "failed" {
		t.Fatalf("release used inference or accepted missing observer: %d %#v", provider.calls, callback.updates)
	}
	if CompletionTokensForOperation("delivery.release_gate") != 0 {
		t.Fatal("release gate received a model budget")
	}
}
