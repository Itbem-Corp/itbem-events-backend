package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Qualification requires real VM execution through the task-scoped adapter.
func TestWorkspaceAcceptsBoundedPinnedJailerSupervisorConfiguration(t *testing.T) {
	command := []string{"python3", "/operator/supervisor.py", "--profile", "production", "--go-sdk-image", "/operator/sdk.ext4", "--go-sdk-sha256", strings.Repeat("a", 64), "--jailer", "--cgroup-parent", "system.slice/worker.service", "--jailer-base", "/operator/jails"}
	if err := validateSandboxSupervisorCommand(command); err != nil {
		t.Fatal(err)
	}
	for len(command) <= 16 {
		command = append(command, "bounded")
	}
	if err := validateSandboxSupervisorCommand(command); err == nil {
		t.Fatal("oversized supervisor configuration accepted")
	}
	if err := validateSandboxSupervisorCommand([]string{"python3", "/operator/supervisor.py", "$(untrusted)"}); err == nil {
		t.Fatal("unsafe operator argument accepted")
	}
}

func TestRealFirecrackerGoToolchainLifecycle(t *testing.T) {
	supervisor := os.Getenv("ITBEM_FIRECRACKER_VSOCK_SUPERVISOR")
	image, digest := os.Getenv("ITBEM_FIRECRACKER_TEST_GO_IMAGE"), os.Getenv("ITBEM_FIRECRACKER_TEST_GO_SHA256")
	if supervisor == "" || image == "" || digest == "" {
		t.Skip("requires operator-pinned Go image and actual Firecracker artifacts")
	}
	supervisorCommand := []string{"python3", supervisor, "--profile", "production", "--go-sdk-image", image, "--go-sdk-sha256", digest}
	if os.Getenv("ITBEM_FIRECRACKER_TEST_JAILER") == "1" {
		parent := strings.TrimSpace(os.Getenv("ITBEM_FIRECRACKER_TEST_CGROUP_PARENT"))
		if parent == "" {
			t.Fatal("jailed Go qualification requires a delegated cgroup parent")
		}
		supervisorCommand = append(supervisorCommand, "--jailer", "--cgroup-parent", parent)
		if base := os.Getenv("ITBEM_FIRECRACKER_TEST_JAILER_BASE"); base != "" {
			supervisorCommand = append(supervisorCommand, "--jailer-base", base)
		}
	}
	for _, name := range []string{"passing", "failing", "boundaries", "qa"} {
		failing := name == "failing"
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if name == "qa" {
				root = setupImplementationRepository(t)
			}
			body := "if 19+23 != 42 { t.Fatal(\"arithmetic\") }"
			imports := `"testing"`
			if failing {
				body = "t.Fatal(\"expected negative qualification\")"
			}
			if name == "boundaries" {
				canary := filepath.Join(t.TempDir(), "host-canary.txt")
				if err := os.WriteFile(canary, []byte("synthetic-host-only"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ITBEM_SYNTHETIC_GUEST_CANARY", "synthetic-host-only")
				imports = `"testing"; "os"; "net"; "time"`
				body = fmt.Sprintf(`
for _, path := range []string{"/workspace/unauthorized", "/sdk/unauthorized", "/unauthorized"} {
 if err := os.WriteFile(path, []byte("synthetic"), 0600); err == nil { t.Fatalf("write accepted: %%s", path) }
}
if os.Getenv("ITBEM_SYNTHETIC_GUEST_CANARY") != "" { t.Fatal("host environment inherited") }
if _, err := os.ReadFile(%q); err == nil { t.Fatal("host canary exposed") }
interfaces, err := net.Interfaces(); if err != nil { t.Fatal(err) }
for _, device := range interfaces { if device.Flags & net.FlagLoopback == 0 { t.Fatal("unexpected guest NIC") } }
connection, err := net.DialTimeout("tcp", "198.51.100.1:443", 200*time.Millisecond)
if err == nil { connection.Close(); t.Fatal("guest network reached reserved test address") }
`, canary)
			}
			for name, text := range map[string]string{
				"go.mod":          "module guest-fixture\n\ngo 1.25.0\n",
				"fixture_test.go": "package fixture\nimport (" + imports + ")\nfunc TestActualGuest(t *testing.T) { " + body + " }\n",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			workspace := Workspace{ID: "real-go-toolchain", Root: root, Config: WorkspaceConfig{
				SandboxRuntime: WorkspaceSandboxFirecracker, RequireSandbox: true,
				SandboxSupervisorCommand: supervisorCommand,
			}}
			ctx := withSandboxTaskID(context.Background(), "task-go-toolchain-"+name)
			if name == "qa" {
				branch := "itbem-agent/c594aa23-06fe-4671-85ce-d31fcd984781"
				worktree := filepath.Join(root, ".itbem-agent-worktrees", strings.TrimPrefix(branch, "itbem-agent/"))
				for _, args := range [][]string{{"add", "go.mod", "fixture_test.go"}, {"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-m", "synthetic guest QA fixture"}, {"worktree", "add", "-b", branch, worktree}} {
					result, err := runLocal(context.Background(), root, time.Minute, "", "git", args...)
					if err != nil || result.ExitCode != 0 {
						t.Fatalf("synthetic Git worktree setup failed: %#v, %v", result, err)
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
				defer server.Close()
				workspace.Config.Path = root
				workspace.Config.ValidationCommands = [][]string{{"go", "test", "-json", "-count=1", "-timeout=30s", "./..."}}
				registry, err := json.Marshal(map[string]WorkspaceConfig{"real-go-toolchain": workspace.Config})
				if err != nil {
					t.Fatal(err)
				}
				metadata, err := json.Marshal(reviewedQAMetadata(t, root, worktree, "HEAD^"))
				if err != nil {
					t.Fatal(err)
				}
				delivery := []byte(`{"work_item":{"preview_url":"` + server.URL + `"},"context_sources":[{"kind":"repository","reference":"workspace://real-go-toolchain"}],"change_sets":[{"repository_ref":"workspace://real-go-toolchain","branch":"` + branch + `","review_type":"local_worktree","ci_status":"passed","metadata":` + string(metadata) + `}],"approved_plan":{"qa_execution_matrix":[{"repository_ref":"workspace://real-go-toolchain","run_validation":true,"run_qa":false,"run_stagehand":false,"collect_evidence":false}]}}`)
				result, _, err := RunQA(ctx, "task-go-toolchain-qa", "run-go-toolchain-qa", delivery, func(key string) string {
					if key == "ITBEM_AI_WORKSPACES_JSON" {
						return string(registry)
					}
					return ""
				})
				if err != nil {
					t.Fatal(err)
				}
				runs, ok := result["repository_runs"].([]any)
				if !ok || len(runs) != 1 {
					t.Fatalf("QA repository evidence missing: %#v", result)
				}
				commands, ok := runs[0].(map[string]any)["commands"].([]any)
				if !ok || len(commands) != 1 {
					t.Fatalf("QA command evidence missing: %#v", result)
				}
				command := commands[0].(map[string]any)
				lease := command["sandbox_lease"].(map[string]any)
				if command["passed"] != true || lease["sandbox_attestation"].(map[string]any)["toolchain_image_sha256"] != digest || lease["sandbox_lifecycle"].(map[string]any)["destroyed"] != true || runs[0].(map[string]any)["branch"] != branch {
					t.Fatalf("QA did not retain guest test evidence: %#v", result)
				}
				return
			}
			result, err := runWorkspaceCommand(ctx, workspace, root, 90*time.Second, "", nil, "go", "test", "-json", "-count=1", "-timeout=30s", "./...")
			if err != nil {
				t.Fatal(err)
			}
			if (result.ExitCode != 0) != failing || !strings.Contains(result.Output, `"Test":"TestActualGuest"`) {
				t.Fatalf("guest test outcome missing or incorrect: %#v", result)
			}
			lifecycle, ok := result.SandboxLease["sandbox_lifecycle"].(map[string]any)
			if !ok || lifecycle["destroyed"] != true || lifecycle["guest_command_executed"] != true || lifecycle["attestation_persisted"] != true {
				t.Fatalf("incomplete lifecycle: %#v", result.SandboxLease)
			}
			attestation, ok := result.SandboxLease["sandbox_attestation"].(map[string]any)
			if !ok || attestation["toolchain_image_sha256"] != digest || attestation["registered_command"] != "go.test.json.offline" {
				t.Fatalf("toolchain pin missing: %#v", result.SandboxLease)
			}
		})
	}
}
