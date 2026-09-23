package automationagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in WSL proof for the task-scoped virtio-vsock supervisor. It is kept
// separate from the serial-console fixture so a passing local fallback cannot
// mask the production transport contract.
func TestRealFirecrackerVirtioVsockSupervisorLifecycle(t *testing.T) {
	supervisor := strings.TrimSpace(os.Getenv("ITBEM_FIRECRACKER_VSOCK_SUPERVISOR"))
	if supervisor == "" {
		t.Skip("set ITBEM_FIRECRACKER_VSOCK_SUPERVISOR to run the real virtio-vsock proof")
	}
	supervisorCommand := []string{"python3", supervisor}
	if profile := strings.TrimSpace(os.Getenv("ITBEM_FIRECRACKER_TEST_PROFILE")); profile != "" {
		if profile != "local" && profile != "production" {
			t.Fatalf("unsupported Firecracker test profile: %s", profile)
		}
		supervisorCommand = append(supervisorCommand, "--profile", profile)
	}
	if os.Getenv("ITBEM_FIRECRACKER_TEST_JAILER") == "1" {
		supervisorCommand = append(supervisorCommand, "--jailer")
	}
	root := t.TempDir()
	marker := "workspace-content-from-control-plane"
	if err := os.WriteFile(filepath.Join(root, "agent-fixture.txt"), []byte(marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{ID: "real-firecracker-vsock", Root: root, Config: WorkspaceConfig{
		SandboxRuntime: WorkspaceSandboxFirecracker, SandboxSupervisorCommand: supervisorCommand, RequireSandbox: true,
	}}
	if err := validateWorkspaceSandbox(&workspace.Config); err != nil {
		t.Fatal(err)
	}
	ctx := withSandboxTaskID(context.Background(), "task-real-firecracker-vsock")
	result, err := runWorkspaceCommand(ctx, workspace, root, 30*time.Second, "", nil, "/bin/cat", "/workspace/agent-fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Output, marker) {
		t.Fatalf("guest worktree file was not observed: %#v", result)
	}
	lifecycle, ok := result.SandboxLease["sandbox_lifecycle"].(map[string]any)
	if !ok || lifecycle["created"] != true || lifecycle["worktree_bound"] != true || lifecycle["guest_command_executed"] != true || lifecycle["destroyed"] != true || lifecycle["attestation_persisted"] != true {
		t.Fatalf("incomplete real virtio-vsock lifecycle: %#v", result.SandboxLease)
	}
	attestation, ok := result.SandboxLease["sandbox_attestation"].(map[string]any)
	if !ok || attestation["runtime"] != "firecracker" || attestation["transport"] != "virtio_vsock" || attestation["evidence_scope"] != "task_guest_command_worktree" || attestation["guest_command_verified"] != true {
		t.Fatalf("real virtio-vsock attestation missing: %#v", result.SandboxLease)
	}
}
