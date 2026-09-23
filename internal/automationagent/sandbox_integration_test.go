package automationagent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDockerSandboxRoundTrip(t *testing.T) {
	if os.Getenv("ITBEM_DOCKER_SANDBOX_E2E") != "1" {
		t.Skip("set ITBEM_DOCKER_SANDBOX_E2E=1 to run the Docker sandbox round-trip")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not available on this worker")
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
	if err != nil || identity.ExitCode != 0 || strings.TrimSpace(identity.Output) != "65532" {
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
}
