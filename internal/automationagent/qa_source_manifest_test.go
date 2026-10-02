package automationagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestQAReviewedSourceManifestRejectsGitInvisibleChanges(t *testing.T) {
	root := setupImplementationRepository(t)
	worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, "a4a4b837-2e18-43af-9f58-6d59629db2bb")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"README.md": "reviewed\n", ".gitignore": "ignored.go\n"} {
		if err := os.WriteFile(filepath.Join(worktree, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	staged, err := runLocal(context.Background(), worktree, commandTimeout, "", "git", "add", "--intent-to-add", ".gitignore")
	if err != nil || staged.ExitCode != 0 {
		t.Fatalf("fixture index: %v / %#v", err, staged)
	}
	metadata := reviewedQAMetadata(t, root, worktree, "HEAD")
	digest, err := sandboxWorktreeDigest(worktree)
	if err != nil {
		t.Fatal(err)
	}
	target := qaTarget{root: worktree, reviewBinding: &publicationAuthorization{Branch: branch, BaseSHA: metadata["base_sha"], ReviewDiffSHA256: metadata["review_diff_sha256"]}, reviewSourceSHA256: fmt.Sprintf("%x", digest)}
	if err := verifyQATargetRevision(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new_test.go", "ignored.go"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(worktree, name)
			if err := os.WriteFile(path, []byte("package fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			if name == "ignored.go" {
				result, err := runLocal(context.Background(), worktree, commandTimeout, "", "git", "check-ignore", name)
				if err != nil || result.ExitCode != 0 {
					t.Fatalf("ignored fixture is not ignored: %v / %#v", err, result)
				}
			}
			// Demonstrate the authority gap: the reviewed Git diff still matches.
			if err := verifyReviewedWorktree(context.Background(), worktree, target.reviewBinding); err != nil {
				t.Fatalf("fixture must remain invisible to Git diff: %v", err)
			}
			if err := verifyQATargetRevision(context.Background(), target); err == nil {
				t.Fatal("unreviewed source accepted")
			}
		})
	}
	if err := verifyQATargetRevision(context.Background(), target); err != nil {
		t.Fatal("restored source rejected", err)
	}
	target.reviewSourceSHA256 = "malformed"
	if err := verifyQATargetRevision(context.Background(), target); err == nil {
		t.Fatal("malformed manifest accepted")
	}
}
