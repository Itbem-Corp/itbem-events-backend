package automationagent

import (
	"context"
	"encoding/json"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/releasegate"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReleaseObserverTransportSignsOnlySealedLeaseAndRun(t *testing.T) {
	identity, instance := newTestMachineIdentity(t)
	const run = "22222222-2222-4222-8222-222222222222"
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		assertSignedCallbackRequest(t, r, body, identity, instance)
		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/internal/automation/gateway/release-observation" || len(payload) != 2 || payload["lease_token"] != "sealed-lease" || payload["run_id"] != run {
			t.Error("observer transmitted an unexpected authority or subject")
		}
		if r.Header.Get("X-Agent-Gateway-Token") != "synthetic-lane-token" || r.Header.Get("X-Agent-Role") != string(agentwork.RoleReleaseManager) || r.Header.Get("X-Agent-Lane") != string(agentwork.LaneRelease) {
			t.Error("observer omitted gateway identity")
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instance)
	gateway, err := NewHTTPGateway(server.URL, "synthetic-lane-token", agentwork.RoleReleaseManager, agentwork.LaneRelease, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), gatewayLeaseContextKey{}, "sealed-lease")
	_, err = callback.ObserveRelease(ctx, gateway, "11111111-1111-4111-8111-111111111111", run, json.RawMessage(`{"worker_candidate":"must-never-be-sent"}`))
	if !called || err == nil {
		t.Fatal("signed observation did not preserve server denial")
	}
}

func TestReleaseObservationClientRejectsChangedSubjectAndInventedApproval(t *testing.T) {
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
	for _, name := range []string{"valid", "sha", "change-set", "approval", "task", "digest", "extra-field"} {
		t.Run(name, func(t *testing.T) {
			observed, env := candidate, environment
			observed.Revisions = append([]releasegate.Revision(nil), candidate.Revisions...)
			switch name {
			case "sha":
				observed.Revisions[0].SHA = strings.Repeat("b", 40)
			case "change-set":
				observed.ChangeSetID = "other"
			case "approval":
				observed.HumanApproval = &releasegate.HumanApproval{Approved: true}
			case "task":
				env.TaskID = "22222222-2222-4222-8222-222222222222"
			case "digest":
				env.MatrixDigest = strings.Repeat("c", 64)
			}
			handoff := releaseGateHandoff(observed, env)
			if name == "extra-field" {
				handoff["invented_authority"] = true
			}
			raw, err := json.Marshal(handoff)
			if err != nil {
				t.Fatal(err)
			}
			_, err = validateReleaseObservation(task, delivery, raw)
			if (name == "valid") != (err == nil) {
				t.Fatalf("unexpected acceptance: %v", err)
			}
		})
	}
}
