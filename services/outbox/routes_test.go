package outbox

import "testing"

func TestRouteForKeepsRuntimeBoundariesExplicit(t *testing.T) {
	tests := []struct {
		eventType string
		runtime   Runtime
	}{
		{analyticsRollupType, RuntimeRustWorker},
		{slackNotificationType, RuntimeRustWorker},
		{automationProcessType, RuntimeLocalAgent},
		{mediaProcessType, RuntimeMediaLambda},
	}
	for _, test := range tests {
		route, err := RouteFor(test.eventType)
		if err != nil {
			t.Fatalf("RouteFor(%q): %v", test.eventType, err)
		}
		if route.Runtime != test.runtime {
			t.Fatalf("RouteFor(%q) runtime = %q, want %q", test.eventType, route.Runtime, test.runtime)
		}
	}
}

func TestRouteForRejectsUnregisteredWorkloads(t *testing.T) {
	if _, err := RouteFor("media.transcode"); err == nil {
		t.Fatal("unregistered workload was accepted")
	}
}

func TestValidateRouteRestrictsLocalAgentToITBEM(t *testing.T) {
	route, err := RouteFor(automationProcessType)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoute(route, "eventiapp"); err == nil {
		t.Fatal("non-ITBEM automation workload was accepted")
	}
	if err := ValidateRoute(route, "itbem"); err != nil {
		t.Fatalf("ITBEM automation workload rejected: %v", err)
	}
}
