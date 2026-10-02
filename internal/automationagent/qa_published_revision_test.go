package automationagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"events-stocks/internal/releasegate"
)

func TestPublishedQATargetRequiresExactCleanCommitAndRejectsUntrackedSource(t *testing.T) {
	root := setupImplementationRepository(t)
	worktree, branch, err := isolatedWorktree(context.Background(), Workspace{Root: root}, "a4a4b837-2e18-43af-9f58-6d59629db2bb")
	if err != nil {
		t.Fatal(err)
	}
	head, err := runLocal(context.Background(), root, commandTimeout, "", "git", "rev-parse", "HEAD")
	if err != nil || head.ExitCode != 0 {
		t.Fatal("fixture revision missing", err)
	}
	registry, err := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root}})
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(registry)
		}
		return ""
	}
	delivery, err := json.Marshal(map[string]any{"change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": strings.TrimSpace(head.Output), "review_type": "pull_request", "ci_status": "passed"}}})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := deliveryQATargets(delivery, lookup)
	if err != nil || len(targets) != 1 || targets[0].reviewCommitSHA != strings.TrimSpace(head.Output) {
		t.Fatalf("exact published target rejected: %#v / %v", targets, err)
	}
	for _, name := range []string{"README.md", "unreviewed_test.go"} {
		path := filepath.Join(worktree, name)
		if err := os.WriteFile(path, []byte("synthetic changed source\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := verifyQATargetRevision(context.Background(), targets[0]); err == nil {
			t.Fatalf("source change accepted: %s", name)
		}
		if name == "README.md" {
			if err := os.WriteFile(path, []byte("base\n"), 0600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	wrong := targets[0]
	wrong.reviewCommitSHA = strings.Repeat("a", 40)
	if err := verifyQATargetRevision(context.Background(), wrong); err == nil {
		t.Fatal("wrong published SHA accepted")
	}
	if err := verifyQATargetRevision(context.Background(), targets[0]); err != nil {
		t.Fatal("restored exact source rejected", err)
	}
	remote, err := runLocal(context.Background(), root, commandTimeout, "", "git", "remote", "add", "origin", "https://github.com/acme/synthetic-qa.git")
	if err != nil || remote.ExitCode != 0 {
		t.Fatal("fixture origin", err, remote)
	}
	revision := releasegate.Revision{Repository: "acme/synthetic-qa", Branch: "main", SHA: strings.TrimSpace(head.Output)}
	matrixPayload := func(revisions []releasegate.Revision) json.RawMessage {
		raw, err := json.Marshal(map[string]any{
			"gatekeeper":  map[string]any{"revisions": revisions},
			"change_sets": []any{map[string]any{"repository_ref": "workspace://repo", "branch": branch, "commit_sha": revision.SHA, "metadata": map[string]string{"remote_repository": revision.Repository, "target_branch": "main"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	want, err := releasegate.RevisionMatrixDigest([]releasegate.Revision{revision})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := qaPublishedMatrixDigest(context.Background(), matrixPayload([]releasegate.Revision{revision}), targets); err != nil || got != want {
		t.Fatalf("published matrix binding: %s %v", got, err)
	}
	for _, field := range []string{"repository", "branch", "sha"} {
		wrong := revision
		switch field {
		case "repository":
			wrong.Repository = "acme/other"
		case "branch":
			wrong.Branch = "other"
		case "sha":
			wrong.SHA = strings.Repeat("b", 40)
		}
		if _, err := qaPublishedMatrixDigest(context.Background(), matrixPayload([]releasegate.Revision{wrong}), targets); err == nil {
			t.Fatalf("wrong matrix %s accepted", field)
		}
	}
	if _, err := qaPublishedMatrixDigest(context.Background(), matrixPayload([]releasegate.Revision{revision}), nil); err == nil {
		t.Fatal("omitted repository accepted")
	}
}
