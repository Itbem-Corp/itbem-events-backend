package automationagent

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirecrackerRejectsSourceChangeAfterLeaseBinding(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.txt")
	if err := os.WriteFile(path, []byte("approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := sandboxWorktreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{ID: "synthetic-workspace", Root: root, Config: WorkspaceConfig{SandboxRuntime: WorkspaceSandboxFirecracker, SandboxSupervisorCommand: []string{"must-not-be-executed"}, RequireSandbox: true}}
	_, err = runFirecrackerSandboxCommand(withSandboxTaskID(context.Background(), "synthetic-task"), workspace, root, "synthetic-lease", "sha256:"+hex.EncodeToString(digest[:]), time.Second, "", nil, "/bin/cat", "/workspace/source.txt")
	if err == nil || !strings.Contains(err.Error(), "changed after the sandbox lease") {
		t.Fatalf("stale lease must fail before invoking the supervisor: %v", err)
	}
}

func TestSandboxWorktreeDigestBindsContentInsteadOfPath(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, root := range []string{a, b} {
		if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("approved\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := sandboxWorktreeDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sandboxWorktreeDigest(b)
	if err != nil || first != other {
		t.Fatal("same source content must have the same digest independent of host path", err)
	}
	if err := os.WriteFile(filepath.Join(a, "source.txt"), []byte("modified\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := sandboxWorktreeDigest(a)
	if err != nil || first == changed {
		t.Fatal("source mutation must invalidate the binding", err)
	}
	if err := os.Rename(filepath.Join(b, "source.txt"), filepath.Join(b, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	renamed, err := sandboxWorktreeDigest(b)
	if err != nil || renamed == first {
		t.Fatal("source filename must be bound", err)
	}
}

func TestSandboxWorktreeDigestRejectsCredentialsAndSymlinks(t *testing.T) {
	for _, name := range []string{".env", ".env.local", "credentials", "signing.key"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, name), []byte("synthetic-canary"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := sandboxWorktreeDigest(root); err == nil {
			t.Fatalf("credential path %q accepted", name)
		}
	}
	for _, name := range []string{".local", ".aws", ".ssh", ".codex", ".config"} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := sandboxWorktreeDigest(root); err == nil {
			t.Fatalf("authority directory %q accepted", name)
		}
	}
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	if _, err := sandboxWorktreeDigest(root); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestSandboxWorktreeDigestExcludesGitAuthority(t *testing.T) {
	root := t.TempDir()
	before, err := sandboxWorktreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "credentials"), []byte("synthetic-never-transfer"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := sandboxWorktreeDigest(root)
	if err != nil || before != after {
		t.Fatal("Git authentication metadata must not be read or transferred", err)
	}
}

func TestSandboxWorktreeDigestMatchesPythonSupervisor(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		candidate, err := exec.LookPath(name)
		if err == nil && exec.Command(candidate, "-c", "import os; assert hasattr(os, 'O_DIRECTORY') and hasattr(os, 'O_NOFOLLOW')").Run() == nil {
			python = candidate
			break
		}
	}
	if python == "" {
		if os.Getenv("ITBEM_REQUIRE_POSIX_SUPERVISOR_PROOF") == "1" {
			t.Fatal("POSIX Python supervisor proof required but its runtime is unavailable")
		}
		t.Skip("Python supervisor runtime unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := sandboxWorktreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := filepath.Abs(filepath.Join("..", "..", "scripts"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(python, "-c", `import sys; sys.path.insert(0,sys.argv[1]); from sandbox_worktree import snapshot_worktree; print(snapshot_worktree(sys.argv[2])[0])`, scripts, root).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("worker/supervisor source binding differs: %s (%v)", output, err)
	}
}
