package automationagent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerSandboxRoundTrip(t *testing.T) {
	if os.Getenv("ITBEM_DOCKER_SANDBOX_E2E") != "1" {
		t.Skip("set ITBEM_DOCKER_SANDBOX_E2E=1 to run the Docker sandbox round-trip")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("Docker qualification was requested but docker is not available on this worker")
	}
	workspace := Workspace{ID: "sandbox", Root: t.TempDir(), Config: WorkspaceConfig{
		SandboxRuntime:     WorkspaceSandboxDocker,
		SandboxImage:       "golang:1.25-bookworm",
		SandboxImageDigest: "sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437",
		SandboxNetwork:     "none",
		RequireSandbox:     true,
	}}
	if err := validateWorkspaceSandbox(&workspace.Config); err != nil {
		t.Fatal(err)
	}
	result, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "go", "version")
	if err != nil || result.ExitCode != 0 || !strings.Contains(result.Output, "linux/amd64") {
		t.Fatalf("docker sandbox round-trip failed: %#v / %v", result, err)
	}
	identity, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "id", "-u")
	expectedUID := strings.Split(dockerSandboxUser(os.Getuid(), os.Getgid()), ":")[0]
	if err != nil || identity.ExitCode != 0 || strings.TrimSpace(identity.Output) != expectedUID || expectedUID == "0" {
		t.Fatalf("docker sandbox must run repository commands as non-root: %#v / %v", identity, err)
	}
	status, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "cat", "/proc/self/status")
	if err != nil || status.ExitCode != 0 || !strings.Contains(status.Output, "NoNewPrivs:\t1") || !strings.Contains(status.Output, "CapEff:\t0000000000000000") {
		t.Fatalf("docker sandbox must drop capabilities and enable no-new-privileges: %#v / %v", status, err)
	}
	network, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "cat", "/proc/net/dev")
	if err != nil || network.ExitCode != 0 || strings.Contains(network.Output, "eth0") {
		t.Fatalf("docker sandbox must not expose an external network interface: %#v / %v", network, err)
	}
	rootWrite, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "sh", "-c", "touch /sandbox-root-must-be-read-only")
	if err != nil || rootWrite.ExitCode == 0 {
		t.Fatalf("docker sandbox root filesystem must be read-only: %#v / %v", rootWrite, err)
	}
	worktreeWrite, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "sh", "-c", "printf ok > /workspace/sandbox-write.txt")
	if err != nil || worktreeWrite.ExitCode != 0 {
		t.Fatalf("docker sandbox must keep only the worktree writable: %#v / %v", worktreeWrite, err)
	}
	t.Setenv("ITBEM_HOST_ONLY_SYNTHETIC_CANARY", "not-forwarded")
	boundary, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 30*time.Second, "", nil, "sh", "-c", `test -z "$ITBEM_HOST_ONLY_SYNTHETIC_CANARY" && test ! -S /var/run/docker.sock && test ! -d /host`)
	if err != nil || boundary.ExitCode != 0 {
		t.Fatalf("sandbox exposed host identity or Docker authority: %#v / %v", boundary, err)
	}
	for name, body := range map[string]string{
		"go.mod":          "module sandbox-fixture\n\ngo 1.25\n",
		"fixture_test.go": "package fixture\nimport \"testing\"\nfunc TestFixture(t *testing.T) { if 2+2 != 4 { t.Fatal(\"arithmetic\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace.Root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	goTest, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 90*time.Second, "", map[string]string{"GOTOOLCHAIN": "local"}, "go", "test", "-count=1", "./...")
	if err != nil || goTest.ExitCode != 0 || !strings.Contains(goTest.Output, "ok") {
		t.Fatalf("sandbox must execute actual repository tests: %#v / %v", goTest, err)
	}
	if err := os.Remove(filepath.Join(workspace.Root, "fixture_test.go")); err != nil {
		t.Fatal(err)
	}
	for _, control := range []string{"fixture", "reference"} {
		for name, source := range map[string]string{
			"go.mod": "fixture/go.mod", "page.go": control + "/page.go",
			"store.go": control + "/store.go", "page_test.go": "oracle/page_test.go",
		} {
			body, err := os.ReadFile(filepath.Join("testdata", "implementation", "pagination-v1", source))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace.Root, name), body, 0600); err != nil {
				t.Fatal(err)
			}
		}
		result, err := runWorkspaceCommand(context.Background(), workspace, workspace.Root, 90*time.Second, "", map[string]string{
			"GOTOOLCHAIN": "local", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off",
		}, "go", "test", "-json", "-count=1", "./...")
		if err != nil {
			t.Fatalf("sandbox implementation control %s: %v", control, err)
		}
		passed, failed := map[string]bool{}, map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(result.Output), "\n") {
			var event struct{ Action, Test string }
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("sandbox control %s emitted malformed test evidence: %v", control, err)
			}
			if event.Action == "pass" {
				passed[event.Test] = true
			}
			if event.Action == "fail" {
				failed[event.Test] = true
			}
		}
		if control == "fixture" {
			if result.ExitCode != 1 || !failed["TestPaginationContract/second"] {
				t.Fatalf("sandbox must reject defective implementation: %#v", result)
			}
			continue
		}
		if result.ExitCode != 0 || len(failed) != 0 {
			t.Fatalf("sandbox reference must pass: %#v", result)
		}
		for _, name := range []string{"first", "second", "partial-tail", "past-tail", "invalid-defaults", "oversized-limit", "overflow-offset", "maximum-size"} {
			if !passed["TestPaginationContract/"+name] {
				t.Fatalf("sandbox reference did not execute oracle case %s: %#v", name, result)
			}
		}
	}
}
