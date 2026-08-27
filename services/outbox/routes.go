package outbox

import (
	"events-stocks/internal/runtimeroute"
)

// Runtime identifies the worker class that owns a durable outbox message.
// It intentionally describes a workload boundary, not a programming language:
// a future Rust or Lambda capability must be explicitly registered before the
// API can publish it.
type Runtime = runtimeroute.Runtime
type Route = runtimeroute.Route

const (
	RuntimeRustWorker  = runtimeroute.RuntimeRustWorker
	RuntimeMediaLambda = runtimeroute.RuntimeMediaLambda
	RuntimeLocalAgent  = runtimeroute.RuntimeLocalAgent
)

const (
	analyticsRollupType   = runtimeroute.AnalyticsRollupType
	slackNotificationType = runtimeroute.SlackNotificationType
	automationProcessType = runtimeroute.AutomationProcessType
	mediaProcessType      = runtimeroute.MediaProcessType
)

// routes is the sole admission point for API-to-runtime handoffs. It prevents
// a new asynchronous feature from accidentally landing on the media or local
// AI queue just because both are available to the API process.
func RouteFor(eventType string) (Route, error) {
	return runtimeroute.RouteFor(eventType)
}

func ValidateRoute(route Route, tenantCode string) error {
	return runtimeroute.Validate(route, tenantCode)
}
