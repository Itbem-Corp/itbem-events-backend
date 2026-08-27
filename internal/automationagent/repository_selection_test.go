package automationagent

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDeliveryRepositoryWorkspaceSelectsExplicitPrimaryFromMultipleSources(t *testing.T) {
	root := t.TempDir()
	registry := fmt.Sprintf(`{"primary":{"path":%q},"supporting":{"path":%q}}`, filepath.ToSlash(root), filepath.ToSlash(root))
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://supporting","metadata":{"repository_role":"supporting"}},{"kind":"repository","reference":"workspace://primary","metadata":{"repository_role":"primary"}}]}`)
	workspace, err := deliveryRepositoryWorkspace(delivery, func(string) string { return registry })
	if err != nil {
		t.Fatalf("select primary workspace: %v", err)
	}
	if workspace.ID != "primary" {
		t.Fatalf("workspace ID = %q, want primary", workspace.ID)
	}
}

func TestDeliveryRepositoryWorkspaceRejectsAmbiguousMultipleSources(t *testing.T) {
	delivery := []byte(`{"context_sources":[{"kind":"repository","reference":"workspace://one"},{"kind":"repository","reference":"workspace://two"}]}`)
	if _, err := deliveryRepositoryWorkspace(delivery, func(string) string { return `{}` }); err == nil {
		t.Fatal("expected ambiguous multi-repository context to be rejected")
	}
}

func TestTopologicalRepositoryOrderRunsDependenciesBeforeConsumers(t *testing.T) {
	ordered, err := topologicalRepositoryOrder(
		[]string{"workspace://frontend", "workspace://worker", "workspace://api"},
		[]repositoryTopologyEntry{
			{Reference: "workspace://frontend", DependsOn: []string{"workspace://api"}},
			{Reference: "workspace://worker", DependsOn: []string{"workspace://api"}},
			{Reference: "workspace://api"},
		},
	)
	if err != nil || !reflect.DeepEqual(ordered, []string{"workspace://api", "workspace://frontend", "workspace://worker"}) {
		t.Fatalf("repository execution ordering must be deterministic and dependency-first: %#v / %v", ordered, err)
	}
	if _, err := topologicalRepositoryOrder(
		[]string{"workspace://api", "workspace://worker"},
		[]repositoryTopologyEntry{
			{Reference: "workspace://api", DependsOn: []string{"workspace://worker"}},
			{Reference: "workspace://worker", DependsOn: []string{"workspace://api"}},
		},
	); err == nil {
		t.Fatal("cyclic repository execution topology must fail closed")
	}
}
