package automationagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrepareDeliveryWorkspacesProvisionsMissingMultiRepositoryBasesConcurrently(t *testing.T) {
	root := t.TempDir()
	registry := map[string]WorkspaceConfig{}
	sources := []map[string]any{}
	for _, id := range []string{"api", "web"} {
		remote := setupImplementationRepository(t)
		branch, err := runLocal(context.Background(), remote, commandTimeout, "", "git", "branch", "--show-current")
		if err != nil || branch.ExitCode != 0 {
			t.Fatalf("branch: %v / %#v", err, branch)
		}
		revision, err := runLocal(context.Background(), remote, commandTimeout, "", "git", "rev-parse", "HEAD")
		if err != nil || revision.ExitCode != 0 {
			t.Fatalf("revision: %v / %#v", err, revision)
		}
		registry[id] = WorkspaceConfig{Path: filepath.Join(root, id), RepositoryURL: remote, BaseBranch: strings.TrimSpace(branch.Output), Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityFetchRemote, WorkspaceCapabilityCreateWorktree}}
		sources = append(sources, map[string]any{"kind": "repository", "reference": "workspace://" + id, "revision": strings.TrimSpace(revision.Output)})
	}
	// An unused missing checkout must not block the selected task.
	registry["unused"] = WorkspaceConfig{Path: filepath.Join(root, "unused"), RepositoryURL: registry["api"].RepositoryURL, BaseBranch: registry["api"].BaseBranch, Capabilities: registry["api"].Capabilities}
	rawRegistry, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(rawRegistry)
		}
		return ""
	}
	delivery, err := json.Marshal(map[string]any{"context_sources": sources})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var group sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() { defer group.Done(); errors <- PrepareDeliveryWorkspaces(ctx, delivery, lookup) }()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"api", "web"} {
		workspace, err := RegisteredWorkspace("workspace://"+id, lookup)
		if err != nil {
			t.Fatal(err)
		}
		for _, taskID := range []string{"a4a4b837-2e18-43af-9f58-6d59629db2bb", "b4a4b837-2e18-43af-9f58-6d59629db2bb"} {
			if _, _, err := isolatedWorktree(ctx, workspace, taskID); err != nil {
				t.Fatal(err)
			}
		}
		if state := ReadWorkspaceGitState(workspace); state.HasLocalChanges {
			t.Fatalf("base was modified by task worktrees: %#v", state)
		}
	}
	if _, err := os.Stat(registry["unused"].Path); !os.IsNotExist(err) {
		t.Fatalf("unused checkout was provisioned: %v", err)
	}
}

func TestManagedWorkspaceLockCancellationAndRelease(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	release, err := lockManagedWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if unlock, err := lockManagedWorkspace(ctx, root); err == nil {
		unlock()
		release()
		t.Fatal("second holder acquired an occupied workspace lock")
	}
	release()
	unlock, err := lockManagedWorkspace(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestProvisionRegisteredWorkspacesPreservesExistingChanges(t *testing.T) {
	root := setupImplementationRepository(t)
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("existing operator change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := json.Marshal(map[string]WorkspaceConfig{"existing": {Path: root, RepositoryURL: "/deliberately-unavailable-origin", BaseBranch: "main", Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityFetchRemote}}})
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return string(registry)
		}
		return ""
	}
	for range 2 {
		if err := ProvisionRegisteredWorkspaces(context.Background(), lookup); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "existing operator change\n" {
		t.Fatalf("startup altered existing work: %q / %v", content, err)
	}
}
