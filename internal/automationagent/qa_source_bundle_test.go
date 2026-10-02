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

func TestQASourceBundlePublishesOnlyAfterPinnedChildVerification(t *testing.T) {
	childSHA, childPack := qaSourcePackFixture(t)
	root := setupImplementationRepository(t)
	git := func(input []byte, args ...string) []byte {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(input)
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte("[submodule \"contract\"]\npath = .contracts/contract\nurl = https://github.com/example/contract.git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(nil, "add", ".gitmodules")
	git(nil, "update-index", "--add", "--cacheinfo", "160000,"+childSHA+",.contracts/contract")
	git(nil, "commit", "-m", "synthetic pinned contract")
	rootSHA := strings.TrimSpace(string(git(nil, "rev-parse", "HEAD")))
	objects := git(nil, "rev-list", "--objects", "--no-object-names", "--no-walk", rootSHA)
	rootPack := git(objects, "pack-objects", "--stdout")
	digest := fmt.Sprintf("%x", sha256.Sum256(rootPack))
	t.Run("producer-authority", func(t *testing.T) {
		calls := 0
		acquire := func(ctx context.Context, repository, commit string) ([]byte, string, error) {
			calls++
			if repository != "example/contract" || commit != childSHA {
				t.Fatal("dependency acquisition substituted repository or original SHA")
			}
			if budget, ok := ctx.Value(qaSourceBundleBudgetKey{}).(*qaSourceBundleBudget); !ok || budget.bytes >= maxQASourceTreeBytes {
				t.Fatal("dependency acquisition did not inherit the parent tree budget")
			}
			return childPack, fmt.Sprintf("%x", sha256.Sum256(childPack)), nil
		}
		if _, _, _, err := buildQASourceBundle(context.Background(), root, rootSHA, nil, acquire); err == nil || calls != 0 {
			t.Fatal("unapproved dependency reached acquisition")
		}
		produced, hash, children, err := buildQASourceBundle(context.Background(), root, rootSHA, map[string]string{".contracts/contract": "example/contract"}, acquire)
		if err != nil || calls != 1 || len(children) != 1 || !validQASourcePackEnvelope(hash, produced) {
			t.Fatalf("approved bundle producer failed: %v", err)
		}
		workspace := Workspace{Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
		if _, err := materializePublishedQASourceBundle(context.Background(), workspace, "itbem-agent/11111111-1111-4111-8111-111111111111", rootSHA, hash, produced, map[string]string{".contracts/contract": "example/contract"}, children); err != nil {
			t.Fatalf("independent importer rejected produced bundle: %v", err)
		}
		if _, _, _, err := buildQASourceBundle(context.Background(), root, rootSHA, map[string]string{".contracts/contract": "example/contract"}, nil); err == nil {
			t.Fatal("missing dependency acquisition was accepted")
		}
	})
	branch := "itbem-agent/11111111-1111-4111-8111-111111111111"
	for _, scenario := range []string{"valid", "unapproved", "wrong-child", "corrupt-child"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := Workspace{Root: t.TempDir(), Config: WorkspaceConfig{RepositoryURL: "https://github.com/example/service.git", BaseBranch: "main"}}
			approved := map[string]string{".contracts/contract": "example/contract"}
			child := packedQASourceDependency{pinnedQASourceDependency: pinnedQASourceDependency{Path: ".contracts/contract", Repository: "example/contract", CommitSHA: childSHA}, PackSHA256: fmt.Sprintf("%x", sha256.Sum256(childPack)), Pack: append([]byte(nil), childPack...)}
			switch scenario {
			case "unapproved":
				approved = map[string]string{}
			case "wrong-child":
				child.CommitSHA = strings.Repeat("a", 40)
			case "corrupt-child":
				child.Pack[len(child.Pack)-1] ^= 1
			}
			target, err := materializePublishedQASourceBundle(context.Background(), workspace, branch, rootSHA, digest, rootPack, approved, []packedQASourceDependency{child})
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if err := verifyQASourceRevision(context.Background(), target, branch, rootSHA); err != nil {
					t.Fatal(err)
				}
				childRoot := filepath.Join(target, ".contracts", "contract")
				if err := verifyQASourceRevision(context.Background(), childRoot, branch, childSHA); err != nil {
					t.Fatal(err)
				}
				if err := verifyQASourceOrigin(context.Background(), childRoot, "https://github.com/example/contract.git"); err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(filepath.Join(childRoot, "unchanged.txt"))
				if err != nil || string(contents) != "stable source\n" {
					t.Fatal("bundle lost unchanged pinned dependency source")
				}
				reused, err := materializePublishedQASourceBundle(context.Background(), workspace, branch, rootSHA, digest, rootPack, approved, []packedQASourceDependency{child})
				if err != nil || reused != target {
					t.Fatalf("exact bundle was not reused: %v", err)
				}
				if err := os.WriteFile(filepath.Join(childRoot, "untracked.txt"), []byte("unapproved source"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := materializePublishedQASourceBundle(context.Background(), workspace, branch, rootSHA, digest, rootPack, approved, []packedQASourceDependency{child}); err == nil {
					t.Fatal("dirty dependency was accepted or repaired")
				}
				body, err := os.ReadFile(filepath.Join(childRoot, "untracked.txt"))
				if err != nil || string(body) != "unapproved source" {
					t.Fatal("rejected bundle reuse changed existing source")
				}
			} else {
				if err == nil {
					t.Fatal("invalid child published an incomplete bundle")
				}
				parent := filepath.Join(workspace.Root, ".itbem-agent-worktrees")
				entries, readErr := os.ReadDir(parent)
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatal(readErr)
				}
				if len(entries) != 0 {
					t.Fatal("failed bundle left a source checkout, stage or lock")
				}
			}
		})
	}
}
