package automationagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in test is the control-plane proof for the local WSL Firecracker
// adapter. It is skipped on ordinary CI because KVM and the fixture artifacts
// are intentionally operator-owned dependencies.
func TestRealFirecrackerSupervisorLifecycle(t *testing.T) {
	supervisor := strings.TrimSpace(os.Getenv("ITBEM_FIRECRACKER_SUPERVISOR"))
	if supervisor == "" {
		t.Skip("set ITBEM_FIRECRACKER_SUPERVISOR to run the real local microVM proof")
	}
	root := t.TempDir()
	workspace := Workspace{ID: "real-firecracker", Root: root, Config: WorkspaceConfig{
		SandboxRuntime: WorkspaceSandboxFirecracker, SandboxSupervisorCommand: []string{"python3", supervisor}, RequireSandbox: true,
	}}
	if err := validateWorkspaceSandbox(&workspace.Config); err != nil {
		t.Fatal(err)
	}
	ctx := withSandboxTaskID(context.Background(), "task-real-firecracker")
	result, err := runWorkspaceCommand(ctx, workspace, root, 30*time.Second, "", nil, "echo", "ITBEM_REAL_CONTROL_PLANE_PASS")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Output, "ITBEM_REAL_CONTROL_PLANE_PASS") {
		t.Fatalf("guest command was not observed: %#v", result)
	}
	lifecycle, ok := result.SandboxLease["sandbox_lifecycle"].(map[string]any)
	if !ok || lifecycle["created"] != true || lifecycle["worktree_bound"] != true || lifecycle["guest_command_executed"] != true || lifecycle["destroyed"] != true || lifecycle["attestation_persisted"] != true {
		t.Fatalf("incomplete real Firecracker lifecycle: %#v", result.SandboxLease)
	}
	attestation, ok := result.SandboxLease["sandbox_attestation"].(map[string]any)
	if !ok || attestation["runtime"] != "firecracker" || attestation["transport"] != "serial_console" || attestation["evidence_scope"] != "local_task_guest_command" || attestation["guest_command_verified"] != true {
		t.Fatalf("real guest attestation missing: %#v", result.SandboxLease)
	}
	if !filepath.IsAbs(supervisor) {
		t.Fatalf("operator supervisor path must be absolute: %s", supervisor)
	}
}
