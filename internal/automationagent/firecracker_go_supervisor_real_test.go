package automationagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Qualification requires real VM execution through the task-scoped adapter.
func TestRealFirecrackerGoToolchainLifecycle(t *testing.T) {
	supervisor := os.Getenv("ITBEM_FIRECRACKER_VSOCK_SUPERVISOR")
	image, digest := os.Getenv("ITBEM_FIRECRACKER_TEST_GO_IMAGE"), os.Getenv("ITBEM_FIRECRACKER_TEST_GO_SHA256")
	if supervisor == "" || image == "" || digest == "" {
		t.Skip("requires operator-pinned Go image and actual Firecracker artifacts")
	}
	for _, failing := range []bool{false, true} {
		name := "passing"
		if failing {
			name = "failing"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			body := "if 19+23 != 42 { t.Fatal(\"arithmetic\") }"
			if failing {
				body = "t.Fatal(\"expected negative qualification\")"
			}
			for name, text := range map[string]string{
				"go.mod":          "module guest-fixture\n\ngo 1.25.0\n",
				"fixture_test.go": "package fixture\nimport \"testing\"\nfunc TestActualGuest(t *testing.T) { " + body + " }\n",
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			workspace := Workspace{ID: "real-go-toolchain", Root: root, Config: WorkspaceConfig{
				SandboxRuntime: WorkspaceSandboxFirecracker, RequireSandbox: true,
				SandboxSupervisorCommand: []string{"python3", supervisor, "--profile", "production", "--go-sdk-image", image, "--go-sdk-sha256", digest},
			}}
			ctx := withSandboxTaskID(context.Background(), "task-go-toolchain-"+name)
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
