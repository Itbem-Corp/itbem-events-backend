package delivery

import (
	"events-stocks/internal/automationagent"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestApplyWorkspaceDeliveryMetadataKeepsSafeArchitectureSignals(t *testing.T) {
	root := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":                   "module example.test/delivery\n",
		"package.json":             "{\"name\":\"delivery-ui\"}\n",
		"cmd/api/main.go":          "package main\nfunc main() {}\n",
		"e2e/delivery.spec.ts":     "export {}\n",
		"docs/architecture.md":     "# Architecture\n",
		"secrets/credentials.json": "must not be inventoried\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	metadata := map[string]any{}
	workspace := automationagent.Workspace{
		ID: "delivery-api", Root: root,
		Config: automationagent.WorkspaceConfig{Capabilities: []string{automationagent.WorkspaceCapabilityReadRepository}},
	}
	applyWorkspaceDeliveryMetadata(metadata, workspace)
	if !reflect.DeepEqual(metadata["workspace_capabilities"], []string{automationagent.WorkspaceCapabilityReadRepository}) || metadata["workspace_harness"] == nil {
		t.Fatalf("workspace capability/harness projection missing: %#v", metadata)
	}
	architecture, ok := metadata["workspace_architecture"].(automationagent.WorkspaceArchitecture)
	if !ok || !reflect.DeepEqual(architecture.RuntimeHints, []string{"go", "node"}) || !reflect.DeepEqual(architecture.EntrypointPaths, []string{"cmd/api/main.go"}) {
		t.Fatalf("safe architecture signals were not projected: %#v", metadata["workspace_architecture"])
	}
	if reflect.DeepEqual(architecture.DocumentationPaths, []string{"secrets/credentials.json"}) {
		t.Fatalf("sensitive inventory file reached project metadata: %#v", architecture)
	}
}
