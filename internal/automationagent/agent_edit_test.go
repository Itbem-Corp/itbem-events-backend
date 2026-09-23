package automationagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentEditProducesValidatedDiffWithoutModelHunkArithmetic(t *testing.T) {
	root := testRepository(t)
	if err := os.WriteFile(filepath.Join(root, "note.go"), []byte("package fixture\nvar Value = 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "fixture")
	registry, _ := json.Marshal(map[string]WorkspaceConfig{"repo": {Path: root}})
	t.Setenv("ITBEM_AI_WORKSPACES_JSON", string(registry))
	_, _, _, _, message, input, _ := agentFixture(t)
	proposal, err := agentFileReplacement(context.Background(), message.Payload.TaskID, input.Delivery, "workspace://repo", "note.go", "package fixture\nvar Value = 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateAgentPatchScope(input.Delivery, proposal); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseChangeProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(parsed.Patches[0].Patch, "@@ -1,2 +1,2 @@") {
		t.Fatal("wrong generated diff")
	}
	for _, path := range []string{"../note.go", ".env", "note.go\nother.go"} {
		if _, err = agentFileReplacement(context.Background(), message.Payload.TaskID, input.Delivery, "workspace://repo", path, "unsafe"); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
