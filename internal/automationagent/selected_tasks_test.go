package automationagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

type selectedTaskClaimRecorder struct{ ids []string }

func (c *selectedTaskClaimRecorder) Update(_ context.Context, id string, _ TaskUpdate) (bool, error) {
	c.ids = append(c.ids, id)
	return false, nil // server denies the claim; selection must not override it
}

func TestSelectedTasksDeferOthersBeforeClaimAndPreserveServerAuthority(t *testing.T) {
	selected, other := uuid.Must(uuid.NewV4()).String(), uuid.Must(uuid.NewV4()).String()
	ids := []string{selected}
	claims := &selectedTaskClaimRecorder{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local", AllowedTaskIDs: ids}, discardStore{}, claims, discardProvider{})
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = other // constructor owns an immutable copy of the operator selection
	message := validMessage()
	message.Payload.TaskID = other
	err = worker.Process(context.Background(), message)
	var retryable *RetryableError
	if !errors.As(err, &retryable) || len(claims.ids) != 0 {
		t.Fatalf("unselected task was claimed or discarded: claims=%v err=%v", claims.ids, err)
	}
	message.Payload.TaskID = selected
	if err := worker.Process(context.Background(), message); err != nil || len(claims.ids) != 1 || claims.ids[0] != selected {
		t.Fatalf("selected task bypassed or missed the server claim: claims=%v err=%v", claims.ids, err)
	}
	worker.config.AllowedOperations = []string{"ai.chat"}
	err = worker.Process(context.Background(), message)
	if !errors.As(err, &retryable) || len(claims.ids) != 1 {
		t.Fatal("selection broadened the role capability")
	}
}

func TestRuntimeSelectedTaskIDsFailClosed(t *testing.T) {
	id := uuid.Must(uuid.NewV4()).String()
	for _, invalid := range []string{"*", id + ",", id + "," + id, strings.ToUpper(id), uuid.Nil.String(), strings.Repeat(id+",", 60) + id} {
		if _, err := LoadRuntimeConfig(runtimeTestEnvironment(t, map[string]string{"ITBEM_AI_TASK_IDS": invalid})); err == nil {
			t.Fatalf("invalid selection accepted: %q", invalid)
		}
	}
	config, err := LoadRuntimeConfig(runtimeTestEnvironment(t, map[string]string{"ITBEM_AI_TASK_IDS": id}))
	if err != nil || len(config.AllowedTaskIDs) != 1 || config.AllowedTaskIDs[0] != id {
		t.Fatalf("valid selection rejected: err=%v", err)
	}
}
