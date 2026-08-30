// Package runtimeroute is the language-neutral registry for durable runtime
// handoffs. It intentionally has no queue client or business dependency, so
// the API, repositories and dispatcher share one auditable admission policy.
package runtimeroute

import (
	"events-stocks/dtos"
	"fmt"
	"strings"
)

// Runtime identifies the execution owner for a durable handoff. It is a
// workload boundary, never a claim that a language owns a product domain.
type Runtime string

const (
	RuntimeRustWorker  Runtime = "rust-worker"
	RuntimeMediaLambda Runtime = "media-lambda"
	RuntimeLocalAgent  Runtime = "local-ai-agent"
)

// Route is safe to persist with an outbox event. QueueNamespace is an audit
// label, not a queue URL or credential.
type Route struct {
	EventType        string
	Runtime          Runtime
	QueueNamespace   string
	NotificationLane bool
	ITBEMOnly        bool
}

const (
	AnalyticsRollupType   = "analytics.rollup"
	SlackNotificationType = "notification.slack"
	AutomationProcessType = "automation.ai.local.process"
	MediaProcessType      = dtos.MediaProcessEventType
)

// routes is the single admission list. Adding asynchronous work requires an
// explicit typed route; no caller can select a runtime from request data.
var routes = map[string]Route{
	AnalyticsRollupType: {
		EventType: AnalyticsRollupType, Runtime: RuntimeRustWorker, QueueNamespace: "rust-worker-prod-workloads",
	},
	SlackNotificationType: {
		EventType: SlackNotificationType, Runtime: RuntimeRustWorker, QueueNamespace: "rust-worker-prod-notifications", NotificationLane: true,
	},
	AutomationProcessType: {
		EventType: AutomationProcessType, Runtime: RuntimeLocalAgent, QueueNamespace: "itbem-ai-local", ITBEMOnly: true,
	},
	MediaProcessType: {
		EventType: MediaProcessType, Runtime: RuntimeMediaLambda, QueueNamespace: "itbem-media",
	},
}

func RouteFor(eventType string) (Route, error) {
	normalized := strings.TrimSpace(eventType)
	route, ok := routes[normalized]
	if !ok {
		return Route{}, fmt.Errorf("unsupported outbox event type %s", normalized)
	}
	return route, nil
}

func Validate(route Route, tenantCode string) error {
	if route.Runtime == "" || strings.TrimSpace(route.QueueNamespace) == "" {
		return fmt.Errorf("outbox route %s has incomplete runtime ownership", route.EventType)
	}
	if route.ITBEMOnly && !strings.EqualFold(strings.TrimSpace(tenantCode), "itbem") {
		return fmt.Errorf("outbox route %s is restricted to ITBEM", route.EventType)
	}
	return nil
}
