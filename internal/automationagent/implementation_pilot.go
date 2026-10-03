package automationagent

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/uuid"
)

//go:embed implementation_pilot_assets/go.mod.txt
var implementationPilotModule []byte

//go:embed implementation_pilot_assets/page_test.go.txt
var implementationPilotOracle []byte

// ExecuteImplementationPilotOracle runs only the evaluator-owned pagination
// oracle. The caller must bind its prepared response and authorize this task;
// this function does not authenticate a provider response or grant admission.
func ExecuteImplementationPilotOracle(ctx context.Context, workspace Workspace, taskID, expectedDigest string) (map[string]any, error) {
	id, err := uuid.FromString(taskID)
	if err != nil || id == uuid.Nil {
		return nil, fmt.Errorf("implementation oracle requires a task UUID")
	}
	if workspace.Config.SandboxRuntime != WorkspaceSandboxDocker || workspace.Config.SandboxNetwork != "none" || !workspace.Config.RequireSandbox {
		return nil, fmt.Errorf("implementation oracle requires isolated Docker without network")
	}
	if err := validateWorkspaceSandbox(&workspace.Config); err != nil {
		return nil, err
	}
	if workspace.Config.SandboxImage != "golang:1.25-bookworm" || workspace.Config.SandboxImageDigest != "sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437" {
		return nil, fmt.Errorf("implementation oracle requires the qualified Go image digest")
	}
	entries, err := os.ReadDir(workspace.Root)
	if err != nil || len(entries) != 4 {
		return nil, fmt.Errorf("implementation oracle requires exactly four prepared files")
	}
	allowed := map[string]bool{"go.mod": true, "page.go": true, "store.go": true, "page_test.go": true}
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(workspace.Root, entry.Name()))
		if err != nil || !allowed[entry.Name()] || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 || info.Size() > 8*1024*1024 {
			return nil, fmt.Errorf("implementation oracle files must be regular prepared files with mode 0644")
		}
	}
	root, err := os.OpenRoot(workspace.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for name, expected := range map[string][]byte{"go.mod": implementationPilotModule, "page_test.go": implementationPilotOracle} {
		actual, err := root.ReadFile(name)
		if err != nil || !bytes.Equal(actual, bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n"))) {
			return nil, fmt.Errorf("implementation oracle evaluator file differs: %s", name)
		}
	}
	digest, err := sandboxWorktreeDigest(workspace.Root)
	if err != nil || expectedDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return nil, fmt.Errorf("implementation oracle source binding mismatch")
	}
	result, err := runWorkspaceCommand(withSandboxTaskID(ctx, taskID), workspace, workspace.Root, 90*time.Second, "", map[string]string{
		"GOTOOLCHAIN": "local", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off",
	}, "go", "test", "-json", "-count=1", "./...")
	if err != nil {
		return nil, err
	}
	if result.SandboxLease["worktree_digest"] != expectedDigest || result.SandboxLease["task_id"] != taskID || result.SandboxLease["status"] != "completed" {
		return nil, fmt.Errorf("implementation oracle execution binding mismatch")
	}
	return map[string]any{"case": "pagination-v1", "exit_code": result.ExitCode, "output": result.Output,
		"sandbox_lease": result.SandboxLease, "model_quality_measured": false, "provider_provenance_authenticated": false}, nil
}
