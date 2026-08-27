package runtimeroute

import "testing"

func TestRegisteredRoutesKeepRuntimeAndProductBoundaries(t *testing.T) {
	tests := []struct {
		eventType string
		runtime   Runtime
		tenant    string
		allowed   bool
	}{
		{AnalyticsRollupType, RuntimeRustWorker, "eventiapp", true},
		{SlackNotificationType, RuntimeRustWorker, "eventiapp", true},
		{MediaProcessType, RuntimeMediaLambda, "eventiapp", true},
		{AutomationProcessType, RuntimeLocalAgent, "itbem", true},
		{AutomationProcessType, RuntimeLocalAgent, "eventiapp", false},
		{AutomationProcessType, RuntimeLocalAgent, "cafetton", false},
	}

	for _, test := range tests {
		t.Run(test.eventType+"/"+test.tenant, func(t *testing.T) {
			route, err := RouteFor(test.eventType)
			if err != nil {
				t.Fatal(err)
			}
			if route.Runtime != test.runtime {
				t.Fatalf("runtime = %q, want %q", route.Runtime, test.runtime)
			}
			err = Validate(route, test.tenant)
			if test.allowed && err != nil {
				t.Fatalf("valid route rejected: %v", err)
			}
			if !test.allowed && err == nil {
				t.Fatal("cross-product route was accepted")
			}
		})
	}
}

func TestRouteForRejectsUnregisteredAsyncWork(t *testing.T) {
	if _, err := RouteFor("automation.arbitrary.shell"); err == nil {
		t.Fatal("unregistered runtime route was accepted")
	}
}
