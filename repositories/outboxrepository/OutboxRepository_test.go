package outboxrepository

import (
	"events-stocks/models"
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
