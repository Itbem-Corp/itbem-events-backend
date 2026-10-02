package automationagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func qaSourcePackFixture(t *testing.T, additions ...func(string) error) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	git := func(input []byte, args ...string) []byte {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("synthetic git %v: %v", args, err)
		}
		return out
	}
	git(nil, "init", "--initial-branch=main")
	git(nil, "config", "user.name", "Synthetic Fixture")
	git(nil, "config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "unchanged.txt"), []byte("stable source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(nil, "add", "unchanged.txt")
	git(nil, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, addition := range additions {
		if err := addition(root); err != nil {
			t.Fatal(err)
		}
	}
	git(nil, "add", ".")
	git(nil, "commit", "-m", "published source")
	commit := strings.TrimSpace(string(git(nil, "rev-parse", "HEAD")))
	// Preserve the real published commit, tree and blobs, including unchanged
	// files, while excluding history. The consumer declares only this SHA shallow.
	objects := git(nil, "rev-list", "--objects", "--no-object-names", "--no-walk", commit)
	pack := git(objects, "pack-objects", "--stdout")
	return commit, pack
}

func TestQASourcePackRejectsExpandedOversizeBeforeCheckout(t *testing.T) {
	commit, pack := qaSourcePackFixture(t, func(root string) error {
		file, err := os.Create(filepath.Join(root, "expanded.bin"))
		if err != nil {
			return err
		}
		defer file.Close()
		return file.Truncate((32 << 20) + 1)
	})
	if len(pack) > 1<<20 {
		t.Fatal("synthetic compression fixture unexpectedly large")
	}
	workspace := Workspace{ID: "synthetic", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	_, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, fmt.Sprintf("%x", sha256.Sum256(pack)), pack)
	if err == nil || !strings.Contains(err.Error(), "expanded tree exceeds") {
		t.Fatalf("compressed oversized source escaped bounds: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspace.Root, ".itbem-agent-worktrees", strings.TrimPrefix(branch, "itbem-agent/"))); !os.IsNotExist(err) {
		t.Fatal("oversized source left a published checkout")
	}
}

func TestQASourcePackRejectsSymlinkBeforePublishingCheckout(t *testing.T) {
	commit, pack := qaSourcePackFixture(t, func(root string) error {
		return os.Symlink("/outside/source", filepath.Join(root, "external-source"))
	})
	workspace := Workspace{ID: "synthetic", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	_, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, fmt.Sprintf("%x", sha256.Sum256(pack)), pack)
	if err == nil || !strings.Contains(err.Error(), "unsupported or unsafe entries") {
		t.Fatalf("source symlink was accepted: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(workspace.Root, ".itbem-agent-worktrees"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected source left staged files or a lock: %v %v", entries, err)
	}
}

func TestQASourcePackCancelledImportLeavesNoCheckoutOrLock(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	workspace := Workspace{ID: "synthetic", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := materializePublishedQASourcePack(ctx, workspace, branch, commit, fmt.Sprintf("%x", sha256.Sum256(pack)), pack); err == nil {
		t.Fatal("cancelled import created a source checkout")
	}
	entries, err := os.ReadDir(filepath.Join(workspace.Root, ".itbem-agent-worktrees"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled import left staged files or a lock: %v %v", entries, err)
	}
	if _, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, fmt.Sprintf("%x", sha256.Sum256(pack)), pack); err != nil {
		t.Fatalf("cancelled import prevented a subsequent authorized import: %v", err)
	}
}

func TestQASourcePackCannotReuseCheckoutThroughSymlink(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	digest := fmt.Sprintf("%x", sha256.Sum256(pack))
	foreign := Workspace{ID: "foreign", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	foreignTarget, err := materializePublishedQASourcePack(context.Background(), foreign, branch, commit, digest, pack)
	if err != nil {
		t.Fatal(err)
	}
	workspace := foreign
	workspace.ID = "synthetic"
	workspace.Root = t.TempDir()
	parent := filepath.Join(workspace.Root, ".itbem-agent-worktrees")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignTarget, filepath.Join(parent, strings.TrimPrefix(branch, "itbem-agent/"))); err != nil {
		t.Skip("symlink unavailable on this host")
	}
	if _, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, digest, pack); err == nil {
		t.Fatal("source provisioner escaped its registered root through a symlink")
	}
	if err := verifyQATargetRevision(context.Background(), qaTarget{root: foreignTarget, branch: branch, reviewCommitSHA: commit}); err != nil {
		t.Fatal("source provisioner changed the foreign checkout")
	}
}

func TestQASourcePackRejectsReusedCheckoutWithSubstitutedOrigin(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	workspace := Workspace{ID: "synthetic", Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	digest := fmt.Sprintf("%x", sha256.Sum256(pack))
	target, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, digest, pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := qaSourceGit(context.Background(), target, nil, "remote", "set-url", "origin", "https://github.com/other/repository.git"); err != nil {
		t.Fatal(err)
	}
	if err := verifyQATargetRevision(context.Background(), qaTarget{root: target, branch: branch, reviewCommitSHA: commit}); err != nil {
		t.Fatal("fixture should remain clean at its exact SHA")
	}
	if _, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, digest, pack); err == nil || !strings.Contains(err.Error(), "origin differs") {
		t.Fatalf("substituted repository was accepted: %v", err)
	}
}

func TestQASourcePackMaterializesOriginalPublishedCommitOnIndependentHost(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	root := t.TempDir()
	workspace := Workspace{ID: "synthetic", Root: root, Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	digest := fmt.Sprintf("%x", sha256.Sum256(pack))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/synthetic-must-not-be-inherited")
	target, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, digest, pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyQATargetRevision(context.Background(), qaTarget{root: target, branch: branch, reviewCommitSHA: commit}); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(filepath.Join(target, "unchanged.txt"))
	if err != nil || string(unchanged) != "stable source\n" {
		t.Fatal("snapshot omitted an unchanged source blob")
	}
	if reused, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, digest, pack); err != nil || reused != target {
		t.Fatalf("exact source was not reused safely: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "untracked.txt"), []byte("mutation"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializePublishedQASourcePack(context.Background(), workspace, branch, commit, digest, pack); err == nil {
		t.Fatal("source provisioner replaced or accepted an existing dirty checkout")
	}
}

func TestQASourcePackRejectsCorruptionWrongCommitAndSymlinkParent(t *testing.T) {
	commit, pack := qaSourcePackFixture(t)
	for _, name := range []string{"digest", "sha", "pack", "parent"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			workspace := Workspace{ID: "synthetic", Root: root, Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
			body := append([]byte(nil), pack...)
			sha := commit
			digest := fmt.Sprintf("%x", sha256.Sum256(body))
			switch name {
			case "digest":
				digest = strings.Repeat("a", 64)
			case "sha":
				sha = strings.Repeat("a", 40)
			case "pack":
				body[len(body)-1] ^= 1
				digest = fmt.Sprintf("%x", sha256.Sum256(body))
			case "parent":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, ".itbem-agent-worktrees")); err != nil {
					t.Skip("symlink unavailable on this host")
				}
			}
			if _, err := materializePublishedQASourcePack(context.Background(), workspace, "itbem-agent/11111111-1111-4111-8111-111111111111", sha, digest, body); err == nil {
				t.Fatal("invalid source pack was materialized")
			}
		})
	}
}
