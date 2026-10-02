package main

import (
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"testing"
)

func TestDoctorPublicationReadinessRequiresRoleSpecificGitHubAppConfiguration(t *testing.T) {
	for _, role := range []struct {
		role, lane  string
		publication bool
	}{
		{"orchestrator", "orchestration", false},
		{"principal_engineer", "engineering", false},
		{"reviewer", "review", true},
		{"qa", "qa", false},
		{"release_manager", "release", true},
	} {
		t.Run(role.lane, func(t *testing.T) {
			config := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.Role(role.role), Lane: agentwork.Lane(role.lane)}}
			for mask := 0; mask < 16; mask++ {
				workspaces, provider, runtime, app := mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0
				want := workspaces && provider && runtime && (!role.publication || app)
				if got := doctorExecutionReady(workspaces, provider, runtime, app, config); got != want {
					t.Fatalf("readiness mask %04b = %v, want %v; publication required=%v", mask, got, want, role.publication)
				}
			}
		})
	}
}

func TestDoctorReadinessRejectsMismatchedAndUnknownWorkerIdentities(t *testing.T) {
	roles := []agentwork.Role{"orchestrator", "principal_engineer", "reviewer", "qa", "release_manager", "", "unknown"}
	lanes := []agentwork.Lane{"orchestration", "engineering", "review", "qa", "release", "", "unknown"}
	for _, role := range roles {
		for _, lane := range lanes {
			if agentwork.IsKnownRoleLane(role, lane) {
				continue
			}
			config := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: role, Lane: lane}}
			for mask := 0; mask < 16; mask++ {
				if doctorExecutionReady(mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0, config) {
					t.Fatalf("invalid role/lane admitted: %q/%q", role, lane)
				}
			}
		}
	}
}

func TestDoctorGatewayReleaseObserverRequiresNoPublicationCredential(t *testing.T) {
	config := automationagent.RuntimeConfig{Transport: "gateway", WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease, AllowedOperations: []string{"delivery.release_gate"}}}
	if githubPublicationRequired(config) || !doctorExecutionReady(true, true, true, false, config) {
		t.Fatal("narrowed gateway observer required a local GitHub credential")
	}
	for _, capabilities := range [][]string{nil, {"delivery.publish"}, {"delivery.release_gate", "delivery.publish"}, {"unknown"}} {
		config.AllowedOperations = capabilities
		if !githubPublicationRequired(config) {
			t.Fatal("publication-capable or unknown profile lost its authority requirement")
		}
	}
	config.AllowedOperations = []string{"delivery.release_gate"}
	config.Transport = "aws"
	if !githubPublicationRequired(config) {
		t.Fatal("legacy transport received gateway-only authority")
	}
}
