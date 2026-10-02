package automationagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRunPublicationRejectsMissingPRCapabilityBeforeAuthentication(t *testing.T) {
	root := t.TempDir()
	registry, err := json.Marshal(map[string]WorkspaceConfig{"repo": {
		Path: root, Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityStageCommit, WorkspaceCapabilityPublishBranch},
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := publicationInput{}
	input.Publication = &publicationAuthorization{
		GrantID: "d4a4b837-2e18-43af-9f58-6d59629db2bb", RepositoryRef: "workspace://repo",
		BaseSHA: strings.Repeat("a", 40), GitHubRepository: "itbem-corp/test-repo",
		ReviewDiffSHA256: strings.Repeat("b", 64), Branch: "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb",
		Capabilities: []string{WorkspaceCapabilityStageCommit, WorkspaceCapabilityPublishBranch, WorkspaceCapabilityCreatePullReq},
		ExpiresAt:    time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
	}
	input.ContextSources = append(input.ContextSources, struct {
		Kind      string         `json:"kind"`
		Reference string         `json:"reference"`
		Metadata  map[string]any `json:"metadata"`
	}{Kind: "repository", Reference: "workspace://repo"})
	delivery, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunPublication(context.Background(), delivery, func(name string) string {
		if name == "ITBEM_AI_WORKSPACES_JSON" {
			return string(registry)
		}
		t.Fatalf("publication accessed configuration %s before capability rejection", name)
		return ""
	})
	if result != nil || err == nil || !strings.Contains(err.Error(), WorkspaceCapabilityCreatePullReq) {
		t.Fatalf("expected early PR capability rejection, got result=%v error=%v", result, err)
	}
}
