package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/releasegate"
	"fmt"
	"strings"
	"testing"
)

type releaseRequestFailureStore struct{ *fakeStore }

func (s releaseRequestFailureStore) PutEncryptedJSON(ctx context.Context, bucket, key string, body []byte) error {
	if strings.HasSuffix(key, "/request.json") {
		return errors.New("synthetic storage unavailable")
	}
	return s.fakeStore.PutEncryptedJSON(ctx, bucket, key, body)
}

func TestReleaseWorkerDefersObservationUntilRequestIsDurable(t *testing.T) {
	input, err := json.Marshal(TaskInput{Delivery: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	store := releaseRequestFailureStore{&fakeStore{input: input, outputBucket: "itbem-ai-outputs-local"}}
	callback, provider := &fakeCallback{operation: "delivery.release_gate"}, &countingProvider{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease}, store, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	observations := 0
	worker.releaseObserver = func(context.Context, string, string, json.RawMessage) (map[string]any, error) {
		observations++
		return nil, errors.New("must not observe")
	}
	message := validMessage()
	message.Payload.Operation = "delivery.release_gate"
	err = worker.Process(context.Background(), message)
	var retryable *RetryableError
	if !errors.As(err, &retryable) || observations != 0 || provider.calls != 0 {
		t.Fatalf("non-durable request reached execution: %v", err)
	}
	for _, update := range callback.updates {
		if update.Status != "running" {
			t.Fatal("storage failure sealed a terminal outcome")
		}
	}
}

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

func TestReleaseWorkerRetriesTransientObservationWithoutSealingFailure(t *testing.T) {
	for _, status := range []int{429, 503, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			input, err := json.Marshal(TaskInput{Delivery: json.RawMessage(`{}`)})
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
			worker.releaseObserver = func(context.Context, string, string, json.RawMessage) (map[string]any, error) {
				return nil, &gatewayRequestError{statusCode: status}
			}
			message := validMessage()
			message.Payload.Operation = "delivery.release_gate"
			err = worker.Process(context.Background(), message)
			var retryable *RetryableError
			if status != 403 {
				if !errors.As(err, &retryable) {
					t.Fatalf("transient failure became terminal: %v", err)
				}
				for _, update := range callback.updates {
					if update.Status == "failed" || update.Status == "completed" {
						t.Fatal("transient observation sealed a terminal callback")
					}
				}
			} else if err != nil || callback.updates[len(callback.updates)-1].Status != "failed" {
				t.Fatal("permanent denial was retried")
			}
			if provider.calls != 0 {
				t.Fatal("observation retry reached provider")
			}
		})
	}
}

func TestReleaseWorkerPreservesDeterministicObservationOnRecovery(t *testing.T) {
	const task = "11111111-1111-4111-8111-111111111111"
	candidate := releasegate.Input{SchemaVersion: releasegate.SchemaVersion, Action: releasegate.ActionRelease, ChangeSetID: task, Revisions: []releasegate.Revision{{Repository: "example/service", Branch: "main", SHA: strings.Repeat("a", 40)}}, Policy: releasegate.Policy{RequiredTestKinds: []string{}}}
	delivery, err := json.Marshal(map[string]any{"gatekeeper": candidate})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	environment := environmentevidence.Observation{SchemaVersion: 1, TaskID: task, MatrixDigest: digest, Repositories: []environmentevidence.Repository{{Repository: "example/service", HeadSHA: strings.Repeat("a", 40), Workflow: ".github/workflows/deploy.yml", Environment: "production", WorkflowExists: true, EnvironmentExists: true, RequiredSecretReferences: []string{}, RequiredVariableReferences: []string{}, MissingSecretReferences: []string{}, MissingVariableReferences: []string{}}}}
	handoff := releaseGateHandoff(candidate, environment)
	input, err := json.Marshal(TaskInput{Prompt: "synthetic release", Delivery: delivery})
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
	observations := 0
	worker.releaseObserver = func(_ context.Context, id, run string, _ json.RawMessage) (map[string]any, error) {
		if id != task || run == "" {
			t.Fatal("observer identity missing")
		}
		observations++
		return handoff, nil
	}
	message := validMessage()
	message.Payload.Operation = "delivery.release_gate"
	message.Payload.TaskID = task
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	last := callback.updates[len(callback.updates)-1]
	if last.Status != "completed" || !last.Deterministic || provider.calls != 0 || observations != 1 || last.CallID != "" || last.ReceiptID != "" {
		t.Fatalf("invalid deterministic completion: %#v", last)
	}
	original, err := json.Marshal(last.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateReleaseObservation(task, delivery, original); err != nil {
		t.Fatal(err)
	}
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	recovered := callback.updates[len(callback.updates)-1]
	raw, err := json.Marshal(recovered.Execution)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := validateReleaseObservation(task, delivery, raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "completed" || !recovered.Deterministic || string(raw) != string(original) || recovered.OutputRef != last.OutputRef || recovered.RequestRef == "" || recovered.RequestRef != last.RequestRef || recovered.RecoveryRunID != last.RunID || observations != 1 || provider.calls != 0 {
		t.Fatalf("recovery repeated observations or changed evidence: %#v calls=%d", recovered, observations)
	}
}
