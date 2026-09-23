package automation

import "testing"

func TestOperationReadinessTreatsEmptyCapabilitiesAsGeneralist(t *testing.T) {
	readiness := operationReadiness([]automationWorkerHealth{
		{Provider: "minimax", Model: "MiniMax-M3", Concurrency: 2},
	})
	for _, lane := range readiness {
		if !lane.Ready || lane.WorkerCount != 1 || lane.WorkerCapacity != 2 {
			t.Fatalf("generalist worker must cover %s: %#v", lane.Operation, lane)
		}
	}
}

func TestOperationReadinessDoesNotInventUnsupportedSpecialties(t *testing.T) {
	readiness := operationReadiness([]automationWorkerHealth{
		{Provider: "minimax", Model: "MiniMax-M3", Concurrency: 1, Capabilities: []string{"code.review"}},
	})
	for _, lane := range readiness {
		if lane.Operation == "code.review" {
			if !lane.Ready || lane.WorkerCount != 1 || lane.WorkerCapacity != 1 {
				t.Fatalf("review specialist should be ready: %#v", lane)
			}
			continue
		}
		if lane.Ready || lane.WorkerCount != 0 || lane.WorkerCapacity != 0 {
			t.Fatalf("unsupported operation %s was presented as ready: %#v", lane.Operation, lane)
		}
	}
}

func TestOperationReadinessIncludesWorkersBeyondThePresentationSample(t *testing.T) {
	workers := make([]automationWorkerHealth, 0, 9)
	for index := 0; index < 8; index++ {
		workers = append(workers, automationWorkerHealth{Concurrency: 1, Draining: true})
	}
	workers = append(workers, automationWorkerHealth{Concurrency: 2, Capabilities: []string{"delivery.qa"}})

	readiness := operationReadiness(workers)
	for _, lane := range readiness {
		if lane.Operation == "delivery.qa" {
			if !lane.Ready || lane.WorkerCount != 1 || lane.WorkerCapacity != 2 {
				t.Fatalf("capable worker beyond the presentation sample must remain visible to readiness: %#v", lane)
			}
			return
		}
	}
	t.Fatal("delivery.qa readiness lane was not returned")
}

func TestEffectiveWorkerCapacityExcludesDrainingAndInvalidWorkers(t *testing.T) {
	got := effectiveWorkerCapacity([]automationWorkerHealth{
		{Concurrency: 3},
		{Concurrency: 7, Draining: true},
		{Concurrency: 0},
		{Concurrency: -2},
	})
	if got != 3 {
		t.Fatalf("effectiveWorkerCapacity = %d, want 3", got)
	}
}

func TestEffectiveWorkerCountExcludesDrainingPresenceFromScaling(t *testing.T) {
	for _, tt := range []struct {
		name             string
		active, draining int64
		want             int64
	}{
		{name: "all available", active: 3, draining: 0, want: 3},
		{name: "one draining", active: 3, draining: 1, want: 2},
		{name: "all draining", active: 3, draining: 3, want: 0},
		{name: "draining cannot exceed active", active: 2, draining: 7, want: 0},
		{name: "negative draining is clamped", active: 2, draining: -1, want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveWorkerCount(tt.active, tt.draining); got != tt.want {
				t.Fatalf("effectiveWorkerCount(%d, %d) = %d, want %d", tt.active, tt.draining, got, tt.want)
			}
		})
	}
}
