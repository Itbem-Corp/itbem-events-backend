package automationagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/agentwork"
	"github.com/gofrs/uuid"
)

func TestWorkspaceCatalogClientSignsRoleBoundedRequest(t *testing.T) {
	identity, instance := newTestMachineIdentity(t)
	entry := WorkspaceCatalogEntry{ProjectID: uuid.Must(uuid.NewV4()).String(), SourceID: uuid.Must(uuid.NewV4()).String(), Repository: "itbem-corp/api", Branch: "main", Revision: strings.Repeat("a", 40), Profile: "go-v1"}
	entry.ID = CatalogWorkspaceID(entry.SourceID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/automation/workspace-catalog" || r.Method != http.MethodGet || r.Header.Get("X-Agent-Role") != "principal_engineer" || r.Header.Get("X-Agent-Lane") != "engineering" || r.Header.Get("X-Agent-Gateway-Token") != "synthetic-role-token" {
			t.Error("catalog request did not carry its exact scope")
		}
		assertSignedCallbackRequest(t, r, nil, identity, instance)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": WorkspaceCatalog{Version: 1, Digest: strings.Repeat("c", 64), ExpiresAt: time.Now().Add(time.Minute), Entries: []WorkspaceCatalogEntry{entry}}})
	}))
	defer server.Close()
	callback := newTestCallbackWithIdentity(t, server, identity, instance)
	catalog, err := callback.FetchWorkspaceCatalog(context.Background(), RuntimeConfig{WorkerConfig: WorkerConfig{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}, GatewayToken: "synthetic-role-token"})
	if err != nil || len(catalog.Entries) != 1 || catalog.Entries[0].ID != entry.ID {
		t.Fatalf("catalog = %#v / %v", catalog, err)
	}
}

func TestWorkspaceCatalogCacheExpiryProfileChangeAndRevocation(t *testing.T) {
	root := t.TempDir()
	cacheFile := filepath.Join(root, "catalog.json")
	profiles := `{"go-v1":{"capabilities":["repository:read","repository:fetch"]}}`
	digest := sha256.Sum256([]byte(profiles))
	cache := workspaceCatalogCache{Catalog: WorkspaceCatalog{Version: 1, ExpiresAt: time.Now().Add(time.Minute)}, Registry: map[string]WorkspaceConfig{"catalog-one": {Path: root}}, ProfileDigest: hex.EncodeToString(digest[:])}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(cache)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cacheFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(key string) string {
		switch key {
		case "ITBEM_AI_WORKSPACE_CATALOG_ENABLED":
			return "true"
		case "ITBEM_AI_WORKSPACE_CATALOG_FILE":
			return cacheFile
		case "ITBEM_AI_WORKSPACE_PROFILES_JSON":
			return profiles
		case "ITBEM_AI_WORKSPACES_JSON":
			return `{}`
		}
		return ""
	}
	write()
	if !json.Valid([]byte(ConfiguredWorkspaceRegistry(lookup))) {
		t.Fatal("fresh catalog unavailable")
	}
	profiles += " "
	if json.Valid([]byte(ConfiguredWorkspaceRegistry(lookup))) {
		t.Fatal("changed execution policy retained cached authority")
	}
	profiles = strings.TrimSpace(profiles)
	cache.Catalog.ExpiresAt = time.Now().Add(-time.Second)
	write()
	if json.Valid([]byte(ConfiguredWorkspaceRegistry(lookup))) {
		t.Fatal("expired catalog retained authority")
	}
	cache.Catalog.ExpiresAt = time.Now().Add(time.Minute)
	cache.Registry = map[string]WorkspaceConfig{}
	write()
	if strings.Contains(ConfiguredWorkspaceRegistry(lookup), "catalog-one") {
		t.Fatal("removed repository remained resolvable")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("revocation deleted retained workspace files")
	}
}

func TestWorkspaceCatalogInvalidPolicyCreatesNoCheckout(t *testing.T) {
	root := t.TempDir()
	entry := WorkspaceCatalogEntry{ProjectID: uuid.Must(uuid.NewV4()).String(), SourceID: uuid.Must(uuid.NewV4()).String(), Repository: "itbem-corp/api", Branch: "main", Revision: strings.Repeat("a", 40), Profile: "go-v1"}
	entry.ID = CatalogWorkspaceID(entry.SourceID)
	catalog := WorkspaceCatalog{Version: 1, ExpiresAt: time.Now().Add(time.Minute), Entries: []WorkspaceCatalogEntry{entry}}
	lookup := func(key string) string {
		switch key {
		case "ITBEM_AI_ROLE":
			return "orchestrator"
		case "ITBEM_AI_WORKSPACE_CATALOG_ROOT":
			return root
		case "ITBEM_AI_WORKSPACE_CATALOG_FILE":
			return filepath.Join(t.TempDir(), "catalog.json")
		case "ITBEM_AI_WORKSPACE_PROFILES_JSON":
			return `{"go-v1":{"capabilities":["repository:read","repository:fetch","patch:apply"]}}`
		}
		return ""
	}
	if err := ReconcileWorkspaceCatalog(context.Background(), catalog, lookup); err == nil {
		t.Fatal("read-only role acquired mutation capability")
	}
	if _, err := os.Stat(filepath.Join(root, entry.ID)); !os.IsNotExist(err) {
		t.Fatalf("invalid profile created checkout: %v", err)
	}
}

func TestWorkspaceCatalogExpiredCacheKeepsTaskQueuedBeforeInference(t *testing.T) {
	t.Setenv("ITBEM_AI_WORKSPACE_CATALOG_ENABLED", "true")
	t.Setenv("ITBEM_AI_WORKSPACE_CATALOG_FILE", filepath.Join(t.TempDir(), "missing.json"))
	provider := &countingProvider{}
	callback := &fakeCallback{}
	worker, err := NewWorker(WorkerConfig{InputBucket: "itbem-ai-inputs-local", OutputBucket: "itbem-ai-outputs-local"}, &fakeStore{}, callback, provider)
	if err != nil {
		t.Fatal(err)
	}
	err = worker.Process(context.Background(), validMessage())
	if _, ok := err.(*RetryableError); !ok || provider.calls != 0 || len(callback.updates) != 0 {
		t.Fatalf("unavailable catalog claimed or spent on task: %v, calls=%d", err, provider.calls)
	}
}

func TestWorkspaceCatalogReconcileKeepsHealthyRepositoriesWhenOneIsUnavailable(t *testing.T) {
	root := t.TempDir()
	cacheFile := filepath.Join(t.TempDir(), "catalog.json")
	entry := WorkspaceCatalogEntry{ProjectID: uuid.Must(uuid.NewV4()).String(), SourceID: uuid.Must(uuid.NewV4()).String(), Repository: "itbem-corp/api", Branch: "main", Profile: "read-v1"}
	entry.ID = CatalogWorkspaceID(entry.SourceID)
	seed := setupImplementationRepository(t)
	entryRoot := filepath.Join(root, entry.ID)
	if err := os.Rename(seed, entryRoot); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"branch", "-M", "main"}, {"remote", "add", "origin", "https://github.com/itbem-corp/api.git"}} {
		result, err := runLocal(context.Background(), entryRoot, commandTimeout, "", "git", args...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("Git setup: %#v / %v", result, err)
		}
	}
	revision, err := runLocal(context.Background(), entryRoot, commandTimeout, "", "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	entry.Revision = strings.TrimSpace(revision.Output)
	blocked := entry
	blocked.SourceID = uuid.Must(uuid.NewV4()).String()
	blocked.ID = CatalogWorkspaceID(blocked.SourceID)
	blocked.Repository = "itbem-corp/unavailable"
	profiles := `{"read-v1":{"capabilities":["repository:read","repository:fetch"]}}`
	lookup := func(key string) string {
		switch key {
		case "ITBEM_AI_ROLE":
			return "orchestrator"
		case "ITBEM_AI_WORKSPACE_CATALOG_ENABLED":
			return "true"
		case "ITBEM_AI_WORKSPACE_CATALOG_ROOT":
			return root
		case "ITBEM_AI_WORKSPACE_CATALOG_FILE":
			return cacheFile
		case "ITBEM_AI_WORKSPACE_PROFILES_JSON":
			return profiles
		case "ITBEM_AI_WORKSPACES_JSON":
			return `{}`
		}
		return ""
	}
	catalog := WorkspaceCatalog{Version: 1, Digest: strings.Repeat("c", 64), ExpiresAt: time.Now().Add(time.Minute), Entries: []WorkspaceCatalogEntry{entry, blocked}}
	if err := ReconcileWorkspaceCatalog(context.Background(), catalog, lookup); err != nil {
		t.Fatal(err)
	}
	workspace, err := RegisteredWorkspace("workspace://"+entry.ID, lookup)
	if err != nil || workspace.Root != entryRoot {
		t.Fatalf("healthy workspace unavailable: %#v / %v", workspace, err)
	}
	if _, err := RegisteredWorkspace("workspace://"+blocked.ID, lookup); err == nil {
		t.Fatal("unprepared workspace reported as runnable")
	}
	raw, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	var cache workspaceCatalogCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		t.Fatal(err)
	}
	if len(cache.Preparation) != 2 || !cache.Preparation[0].Ready || cache.Preparation[1].Ready {
		t.Fatalf("invalid preparation projection: %#v", cache.Preparation)
	}
	catalog.Entries = nil
	if err := ReconcileWorkspaceCatalog(context.Background(), catalog, lookup); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisteredWorkspace("workspace://"+entry.ID, lookup); err == nil {
		t.Fatal("removed repository remained assignable")
	}
	if _, err := os.Stat(filepath.Join(entryRoot, "README.md")); err != nil {
		t.Fatal("revocation destroyed repository evidence")
	}
}
