package automationagent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

func implementationOracleTestWorkspace(t *testing.T, control string) (Workspace, string) {
	t.Helper()
	w := Workspace{ID: "implementation-pilot", Root: t.TempDir(), Config: WorkspaceConfig{
		SandboxRuntime: WorkspaceSandboxDocker, SandboxNetwork: "none", RequireSandbox: true,
		SandboxImage: "golang:1.25-bookworm", SandboxImageDigest: "sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437",
	}}
	for name, source := range map[string]string{"go.mod": "fixture/go.mod", "page.go": control + "/page.go", "store.go": control + "/store.go", "page_test.go": "oracle/page_test.go"} {
		body, err := os.ReadFile(filepath.Join("testdata", "implementation", "pagination-v1", source))
		if err != nil {
			t.Fatal(err)
		}
		if name == "go.mod" || name == "page_test.go" {
			expected := implementationPilotModule
			if name == "page_test.go" {
				expected = implementationPilotOracle
			}
			if !bytes.Equal(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n"))) {
				t.Fatalf("embedded evaluator file differs from benchmark: %s", name)
			}
		}
		if err := os.WriteFile(filepath.Join(w.Root, name), bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n")), 0644); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := sandboxWorktreeDigest(w.Root)
	if err != nil {
		t.Fatal(err)
	}
	return w, "sha256:" + hex.EncodeToString(digest[:])
}

func TestImplementationPilotOracleRejectsUnboundExecution(t *testing.T) {
	for _, mutation := range []string{"task", "runtime", "network", "image", "digest", "oracle", "module", "extra", "executable"} {
		t.Run(mutation, func(t *testing.T) {
			if mutation == "executable" && runtime.GOOS == "windows" {
				t.Skip("POSIX execution modes require Linux")
			}
			workspace, digest := implementationOracleTestWorkspace(t, "reference")
			task := uuid.Must(uuid.NewV4()).String()
			switch mutation {
			case "task":
				task = "arbitrary-task"
			case "runtime":
				workspace.Config.SandboxRuntime = WorkspaceSandboxProcess
			case "network":
				workspace.Config.SandboxNetwork = "bridge"
			case "image":
				workspace.Config.SandboxImageDigest = "sha256:" + strings.Repeat("a", 64)
			case "digest":
				digest = "sha256:" + strings.Repeat("a", 64)
			case "oracle", "module", "extra":
				name := map[string]string{"oracle": "page_test.go", "module": "go.mod", "extra": "extra.go"}[mutation]
				if err := os.WriteFile(filepath.Join(workspace.Root, name), []byte("altered\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "executable":
				if err := os.Chmod(filepath.Join(workspace.Root, "page.go"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ExecuteImplementationPilotOracle(context.Background(), workspace, task, digest); err == nil {
				t.Fatal("unbound execution accepted")
			}
		})
	}
}

func TestImplementationPilotOracleDockerRoundTrip(t *testing.T) {
	if os.Getenv("ITBEM_DOCKER_SANDBOX_E2E") != "1" {
		t.Skip("set ITBEM_DOCKER_SANDBOX_E2E=1 for isolated oracle execution")
	}
	for _, control := range []string{"fixture", "reference"} {
		workspace, digest := implementationOracleTestWorkspace(t, control)
		task := uuid.Must(uuid.NewV4()).String()
		result, err := ExecuteImplementationPilotOracle(context.Background(), workspace, task, digest)
		if err != nil {
			t.Fatal(err)
		}
		result["control"] = control
		evidence, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("implementation oracle execution evidence: %s", evidence)
		lease := result["sandbox_lease"].(map[string]any)
		if lease["task_id"] != task || lease["worktree_digest"] != digest || lease["status"] != "completed" {
			t.Fatalf("missing execution binding: %#v", result)
		}
		output := result["output"].(string)
		passed, failed := map[string]bool{}, map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			var event struct{ Action, Test string }
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			if event.Action == "pass" {
				passed[event.Test] = true
			}
			if event.Action == "fail" {
				failed[event.Test] = true
			}
		}
		if control == "fixture" {
			if result["exit_code"] != 1 || !failed["TestPaginationContract/second"] {
				t.Fatalf("fixture not rejected: %#v", result)
			}
		} else if result["exit_code"] != 0 || len(failed) != 0 {
			t.Fatalf("reference did not pass: %#v", result)
		} else {
			for _, name := range []string{"first", "second", "partial-tail", "past-tail", "invalid-defaults", "oversized-limit", "overflow-offset", "maximum-size"} {
				if !passed["TestPaginationContract/"+name] {
					t.Fatalf("oracle case missing: %s", name)
				}
			}
		}
	}
}
