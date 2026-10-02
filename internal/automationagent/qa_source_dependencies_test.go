package automationagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQASourceDependenciesRequireApprovedRepositoryAndOriginalGitlinkSHA(t *testing.T) {
	for _, scenario := range []struct {
		name, url string
		approved  bool
		valid     bool
	}{
		{"approved", "https://github.com/example/contract.git", true, true},
		{"unapproved", "https://github.com/example/contract.git", false, false},
		{"substitution", "https://github.com/other/contract.git", true, false},
		{"credential", "https://synthetic-secret@github.com/example/contract.git", true, false},
		{"outside", "https://outside.invalid/example/contract.git", true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := setupImplementationRepository(t)
			git := func(args ...string) string {
				cmd := exec.Command("git", args...)
				cmd.Dir = root
				output, err := cmd.Output()
				if err != nil {
					t.Fatalf("synthetic Git %s failed: %v", args[0], err)
				}
				return strings.TrimSpace(string(output))
			}
			pinned := git("rev-parse", "HEAD")
			declaration := "[submodule \"contract\"]\npath = .contracts/contract\nurl = " + scenario.url + "\n"
			if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(declaration), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", ".gitmodules")
			git("update-index", "--add", "--cacheinfo", "160000,"+pinned+",.contracts/contract")
			git("commit", "-m", "synthetic pinned dependency")
			commit := git("rev-parse", "HEAD")
			// The uncommitted module declaration cannot replace the frozen blob.
			if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte("mutated source declaration"), 0600); err != nil {
				t.Fatal(err)
			}
			approved := map[string]string{}
			if scenario.approved {
				approved[".contracts/contract"] = "example/contract"
			}
			dependencies, err := readPinnedQASourceDependencies(context.Background(), root, commit, approved)
			if scenario.valid {
				if err != nil || len(dependencies) != 1 || dependencies[0].CommitSHA != pinned || dependencies[0].Repository != "example/contract" || dependencies[0].Path != ".contracts/contract" {
					t.Fatalf("frozen approved gitlink failed: %v %v", dependencies, err)
				}
			} else if err == nil {
				t.Fatal("unapproved module authority accepted")
			}
		})
	}
}
