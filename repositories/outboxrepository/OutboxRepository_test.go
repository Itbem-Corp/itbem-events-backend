package outboxrepository

import (
	"encoding/json"
	"events-stocks/models"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryDelayBacksOffAndCaps(t *testing.T) {
	assert.Equal(t, 2*time.Second, retryDelay(1))
	assert.Equal(t, 10*time.Second, retryDelay(2))
	assert.Equal(t, 30*time.Second, retryDelay(3))
	assert.Equal(t, 2*time.Minute, retryDelay(4))
	assert.Equal(t, 2*time.Minute, retryDelay(100))
}

func TestApplyRouteFreezesRegisteredRuntimeOwnership(t *testing.T) {
	event := &models.OutboxEvent{EventType: "automation.ai.local.process", TenantCode: "itbem"}
	if err := ApplyRoute(event); err != nil {
		t.Fatalf("apply registered route: %v", err)
	}
	if event.TargetRuntime != "local-ai-agent" || event.QueueNamespace != "itbem-ai-local" {
		t.Fatalf("unexpected durable route labels: %#v", event)
	}
	if err := ApplyRoute(&models.OutboxEvent{EventType: "automation.ai.local.process", TenantCode: "eventiapp"}); err == nil {
		t.Fatal("ITBEM-only runtime must reject another tenant at enqueue time")
	}
	if err := ApplyRoute(&models.OutboxEvent{EventType: "analytics.rollup", TenantCode: "eventiapp", TargetRuntime: "media-lambda"}); err == nil {
		t.Fatal("a caller must not override the registered runtime")
	}
}

func TestPickFairBatchRoundRobinsProjectAndOperationLanes(t *testing.T) {
	newEvent := func(project, operation, job string) models.OutboxEvent {
		return models.OutboxEvent{
			EventType:  "automation.ai.local.process",
			TenantCode: "itbem",
			Payload:    `{"payload":{"project_id":"` + project + `","operation":"` + operation + `"},"job_id":"` + job + `"}`,
		}
	}
	candidates := []models.OutboxEvent{
		newEvent("project-a", "delivery.plan", "a-1"),
		newEvent("project-a", "delivery.plan", "a-2"),
		newEvent("project-a", "delivery.plan", "a-3"),
		newEvent("project-b", "delivery.plan", "b-1"),
		newEvent("project-b", "delivery.plan", "b-2"),
		newEvent("project-a", "delivery.qa", "a-q1"),
	}
	selected := pickFairBatch(candidates, 5)
	if len(selected) != 5 {
		t.Fatalf("selected %d events, want 5", len(selected))
	}
	// A busy project cannot consume the first five slots: all three available
	// lanes receive work before their oldest lane gets a second turn.
	got := make([]string, 0, len(selected))
	for _, event := range selected {
		var payload automationFairnessPayload
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			t.Fatalf("decode selected payload: %v", err)
		}
		got = append(got, payload.Payload.ProjectID+"/"+payload.Payload.Operation)
	}
	want := []string{
		"project-a/delivery.plan",
		"project-a/delivery.qa",
		"project-b/delivery.plan",
		"project-a/delivery.plan",
		"project-b/delivery.plan",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fair selection = %#v, want %#v", got, want)
	}
}

func TestFairnessNormalizationLimitIsBounded(t *testing.T) {
	if got := fairnessNormalizationLimit(20); got != 80 {
		t.Fatalf("candidate window = %d, want 80", got)
	}
	if got := fairnessNormalizationLimit(1000); got != 400 {
		t.Fatalf("candidate window for large batch = %d, want 400", got)
	}
}

func TestRotateFairnessKeysStartsAfterDurableCursorAndWraps(t *testing.T) {
	keys := []string{"project-a/plan", "project-b/plan", "project-c/qa"}
	got := rotateFairnessKeys(keys, "project-b/plan")
	want := []string{"project-c/qa", "project-a/plan", "project-b/plan"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rotated keys = %#v, want %#v", got, want)
	}
	got = rotateFairnessKeys(keys, "project-z/unknown")
	want = []string{"project-a/plan", "project-b/plan", "project-c/qa"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapped keys = %#v, want %#v", got, want)
	}
}

func TestFairnessKeyUsesValidatedProjectAndOperation(t *testing.T) {
	event := models.OutboxEvent{
		EventType:  "automation.ai.local.process",
		TenantCode: "itbem",
		Payload:    `{"payload":{"project_id":" project-42 ","operation":" delivery.qa "}}`,
	}
	if got := fairnessKey(event); got != "automation:project-42:delivery.qa" {
		t.Fatalf("fairness key = %q", got)
	}
	legacy := models.OutboxEvent{EventType: "analytics.rollup", TenantCode: "eventiapp"}
	if got := fairnessKey(legacy); got != "event:eventiapp:analytics.rollup" {
		t.Fatalf("legacy fairness key = %q", got)
	}
}
