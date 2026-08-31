package automationagent

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/internal/agentwork"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunOnboardingCapabilityProbesUsesExactSHAOperatorCommandsAndCleansUp(t *testing.T) {
	workspace, revision, lookup := testOnboardingProbeWorkspace(t, false)
	delivery, err := BuildOnboardingProbeDelivery("workspace://service", "github://acme/service", "trunk", revision, []string{"unit", "integration"})
	if err != nil {
		t.Fatal(err)
	}
	taskID := "11111111-1111-4111-8111-111111111111"
	privateResult, execution, err := RunOnboardingCapabilityProbes(context.Background(), taskID, delivery, lookup)
	if err != nil {
		t.Fatalf("exact-SHA probe failed: %v", err)
	}
	if execution.TaskID != taskID || execution.RepositoryReference != "github://acme/service" || execution.Revision != revision || execution.ExecutorRole != "qa" || len(execution.Probes) != 2 {
		t.Fatalf("unexpected probe handoff: %#v", execution)
	}
	states := map[string]string{}
	for _, probe := range execution.Probes {
		states[probe.Name] = probe.State
		if len(probe.EvidenceSHA256) != 64 || len(probe.SubjectSHA256) != 64 || probe.ExecutorRole != "qa" {
			t.Fatalf("probe is not sealed: %#v", probe)
		}
	}
	if states["unit"] != "ready" || states["integration"] != "blocked" {
		t.Fatalf("configured and missing commands were not distinguished: %#v", states)
	}
	encoded, _ := json.Marshal(privateResult)
	if strings.Contains(string(encoded), filepath.Clean(workspace.Root)) {
		t.Fatal("private probe result exposed the local workspace path")
	}
	if !strings.Contains(string(encoded), `"redacted_output"`) {
		t.Fatal("private probe result omitted bounded diagnostic output")
	}
	executionRaw, _ := json.Marshal(execution)
	if strings.Contains(string(executionRaw), "redacted_output") {
		t.Fatal("public probe callback exposed private diagnostic output")
	}
	decoded, err := DecodeOnboardingProbeExecution(executionRaw)
	if err != nil || decoded.TaskID != taskID {
		t.Fatalf("valid callback projection rejected: %#v / %v", decoded, err)
	}
	probeDirectory := filepath.Join(workspace.Root, ".itbem-agent-worktrees", "probe-"+taskID)
	if _, err := os.Stat(probeDirectory); !os.IsNotExist(err) {
		t.Fatalf("ephemeral probe worktree was retained: %v", err)
	}
	head, err := runLocal(context.Background(), workspace.Root, commandTimeout, "", "git", "rev-parse", "HEAD")
	if err != nil || head.ExitCode != 0 || strings.TrimSpace(head.Output) != revision {
		t.Fatalf("probe changed the base checkout: %#v / %v", head, err)
	}
}

func TestWorkerRunsOnboardingProbeDeterministicallyWithoutProvider(t *testing.T) {
	_, revision, lookup := testOnboardingProbeWorkspace(t, false)
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", lookup("ITBEM_AI_WORKSPACES_JSON"))
	delivery, err := BuildOnboardingProbeDelivery("workspace://service", "github://acme/service", "trunk", revision, []string{"unit"})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(TaskInput{Delivery: delivery})
	store, callback := &fakeStore{input: input}, &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "acme-private-inputs", OutputBucket: "acme-private-evidence", Role: agentwork.RoleQA, Lane: agentwork.LaneQA}, store, callback, fakeProvider{err: errors.New("provider must not be called")})
	if err != nil {
		t.Fatal(err)
	}
	message := TaskMessage{SchemaVersion: 1, JobID: "job", TenantCode: "itbem", CorrelationID: "onboarding", Type: "ai.local.process"}
	message.Payload.TaskID = "33333333-3333-4333-8333-333333333333"
	message.Payload.Operation = agentwork.OperationDeliveryOnboardingProbe
	message.Payload.InputRef = "s3://acme-private-inputs/automation/inputs/33333333-3333-4333-8333-333333333333/input.json"
	message.Payload.Attempt = 1
	if err := worker.Process(context.Background(), message); err != nil {
		t.Fatalf("deterministic onboarding probe invoked provider or failed: %v", err)
	}
	if len(callback.updates) != 2 || callback.updates[1].Status != "completed" || !callback.updates[1].Deterministic || callback.updates[1].Provider != "" || len(callback.updates[1].Execution) == 0 {
		t.Fatalf("deterministic completion was not reported correctly: %#v", callback.updates)
	}
}

func TestRunOnboardingCapabilityProbesRejectsTrackedMutation(t *testing.T) {
	workspace, revision, lookup := testOnboardingProbeWorkspace(t, true)
	delivery, err := BuildOnboardingProbeDelivery("workspace://service", "github://acme/service", "trunk", revision, []string{"unit"})
	if err != nil {
		t.Fatal(err)
	}
	taskID := "22222222-2222-4222-8222-222222222222"
	if _, _, err := RunOnboardingCapabilityProbes(context.Background(), taskID, delivery, lookup); err == nil || !strings.Contains(err.Error(), "modified tracked repository content") {
		t.Fatalf("tracked mutation was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Root, ".itbem-agent-worktrees", "probe-"+taskID)); !os.IsNotExist(err) {
		t.Fatalf("failed probe worktree was retained: %v", err)
	}
}

func TestOnboardingProbeInputCannotSupplyCommandsOrPrivilegedCapabilities(t *testing.T) {
	revision := strings.Repeat("a", 40)
	if _, err := BuildOnboardingProbeDelivery("workspace://service", "github://acme/service", "trunk", revision, []string{"branch_write"}); err == nil {
		t.Fatal("GitHub write authority was accepted as a local command probe")
	}
	raw := json.RawMessage(`{"onboarding_probe":{"schema_version":1,"workspace_reference":"workspace://service","repository_reference":"github://acme/service","default_branch":"trunk","revision":"` + revision + `","capabilities":["unit"],"command":["go","test","./..."]}}`)
	if _, err := decodeOnboardingProbeDelivery(raw); err == nil {
		t.Fatal("task-supplied command was accepted")
	}
}

func testOnboardingProbeWorkspace(t *testing.T, mutate bool) (Workspace, string, func(string) string) {
	t.Helper()
	root := t.TempDir()
	bare, seed, checkout := filepath.Join(root, "remote.git"), filepath.Join(root, "seed"), filepath.Join(root, "checkout")
	for _, command := range [][]string{{"git", "init", "--bare", bare}, {"git", "init", "-b", "trunk", seed}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git fixture setup failed: %#v / %v", result, err)
		}
	}
	for _, command := range [][]string{{"git", "config", "user.email", "probe@example.invalid"}, {"git", "config", "user.name", "Probe Fixture"}} {
		result, err := runLocal(context.Background(), seed, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git fixture identity failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(seed, "go.mod"), []byte("module example.com/probe\n\ngo 1.24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("probe fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, ".gitignore"), []byte(".fixtures/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureTest := "package probe\nimport (\"os\"; \"testing\")\nfunc TestFixture(t *testing.T){ value, err := os.ReadFile(\".fixtures/policy.txt\"); if err != nil || string(value) != \"approved\\n\" { t.Fatalf(\"fixture unavailable: %q / %v\", value, err) } }\n"
	if err := os.WriteFile(filepath.Join(seed, "fixture_test.go"), []byte(fixtureTest), 0600); err != nil {
		t.Fatal(err)
	}
	if mutate {
		if err := os.MkdirAll(filepath.Join(seed, "cmd", "mutate"), 0700); err != nil {
			t.Fatal(err)
		}
		program := "package main\nimport \"os\"\nfunc main(){ _ = os.WriteFile(\"README.md\", []byte(\"changed\\n\"), 0600) }\n"
		if err := os.WriteFile(filepath.Join(seed, "cmd", "mutate", "main.go"), []byte(program), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range [][]string{{"git", "add", "."}, {"git", "commit", "-m", "fixture"}, {"git", "remote", "add", "origin", bare}, {"git", "push", "-u", "origin", "trunk"}} {
		result, err := runLocal(context.Background(), seed, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git fixture commit failed: %#v / %v", result, err)
		}
	}
	cloned, err := runLocal(context.Background(), root, commandTimeout, "", "git", "clone", "--branch", "trunk", bare, checkout)
	if err != nil || cloned.ExitCode != 0 {
		t.Fatalf("git fixture clone failed: %#v / %v", cloned, err)
	}
	repositoryURL := "https://github.com/acme/service.git"
	if changed, err := runLocal(context.Background(), checkout, commandTimeout, "", "git", "remote", "set-url", "origin", repositoryURL); err != nil || changed.ExitCode != 0 {
		t.Fatalf("fixture origin setup failed: %#v / %v", changed, err)
	}
	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(bare)}).String()
	if rewritten, err := runLocal(context.Background(), checkout, commandTimeout, "", "git", "config", "url."+fileURL+".insteadOf", repositoryURL); err != nil || rewritten.ExitCode != 0 {
		t.Fatalf("fixture URL rewrite failed: %#v / %v", rewritten, err)
	}
	fixture := filepath.Join(checkout, ".fixtures", "policy.txt")
	if err := os.MkdirAll(filepath.Dir(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, []byte("approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	head, err := runLocal(context.Background(), checkout, commandTimeout, "", "git", "rev-parse", "HEAD")
	if err != nil || head.ExitCode != 0 {
		t.Fatal("fixture HEAD unavailable")
	}
	command := []string{"go", "test", "./..."}
	if mutate {
		command = []string{"go", "run", "./cmd/mutate"}
	}
	config := map[string]WorkspaceConfig{"service": {
		Path: checkout, RepositoryURL: repositoryURL, BaseBranch: "trunk",
		Capabilities:       []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityFetchRemote, WorkspaceCapabilityCreateWorktree},
		ValidationCommands: [][]string{command}, ValidationCommandKinds: []string{"unit"},
		ReadOnlyFixturePaths: []string{".fixtures"},
	}}
	raw, _ := json.Marshal(config)
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(raw)
		}
		return ""
	}
	workspace, err := RegisteredWorkspace("workspace://service", lookup)
	if err != nil {
		t.Fatal(err)
	}
	return workspace, strings.TrimSpace(head.Output), lookup
}
