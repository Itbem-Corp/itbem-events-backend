package automationagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStageAndCommitPublicationRefusesAnUnmergedWorktree(t *testing.T) {
	root := testRepository(t)
	if err := writeTestFile(root, "shared.txt", "base\n"); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "shared.txt")
	testGit(t, root, "commit", "-m", "initial")
	base := testGit(t, root, "rev-parse", "HEAD")
	branch := "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb"
	testGit(t, root, "checkout", "-b", branch)
	if err := writeTestFile(root, "shared.txt", "agent change\n"); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "shared.txt")
	testGit(t, root, "commit", "-m", "agent change")
	testGit(t, root, "checkout", "-b", "other", base)
	if err := writeTestFile(root, "shared.txt", "operator change\n"); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "shared.txt")
	testGit(t, root, "commit", "-m", "operator change")
	testGit(t, root, "checkout", branch)

	merge, err := runLocal(context.Background(), root, time.Minute, "", "git", "merge", "other")
	if err != nil || merge.ExitCode == 0 {
		t.Fatalf("expected a real merge conflict, got %#v / %v", merge, err)
	}
	status := testGit(t, root, "status", "--porcelain")
	if !strings.Contains(status, "UU shared.txt") {
		t.Fatalf("fixture did not leave an unmerged index: %q", status)
	}

	auth := &publicationAuthorization{
		BaseSHA:          base,
		ReviewDiffSHA256: strings.Repeat("b", 64),
		Branch:           branch,
	}
	_, _, publishErr := stageAndCommitPublication(context.Background(), root, auth, "conflicted change")
	if publishErr == nil || !strings.Contains(publishErr.Error(), "unexpected staged changes") {
		t.Fatalf("unmerged worktree was not rejected before commit: %v", publishErr)
	}
	if got := testGit(t, root, "rev-parse", "HEAD"); got != testGit(t, root, "rev-parse", branch) {
		t.Fatalf("conflict handling changed the branch head: %s", got)
	}
	if statusAfter := testGit(t, root, "status", "--porcelain"); !strings.Contains(statusAfter, "UU shared.txt") {
		t.Fatalf("conflict was hidden or resolved unexpectedly: %q", statusAfter)
	}
}

func writeTestFile(root, name, contents string) error {
	return os.WriteFile(filepath.Join(root, name), []byte(contents), 0600)
}
