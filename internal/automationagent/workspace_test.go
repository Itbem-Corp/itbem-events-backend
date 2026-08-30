package automationagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadWorkspacesProvidesBoundedSecretFreeContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("safe context"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("must not appear"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := `{"demo":{"path":"` + filepath.ToSlash(root) + `","validation_commands":[["go","test","./..."]]}}`
	workspaces, err := LoadWorkspaces(raw)
	if err != nil {
		t.Fatal(err)
	}
	context, err := DescribeWorkspace(workspaces["demo"], []string{"readme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Excerpts) != 1 || context.Excerpts[0].Content != "safe context" {
		t.Fatalf("unexpected context: %#v", context)
	}
	for _, file := range context.Files {
		if file == ".env" {
			t.Fatal("secret file leaked into context")
		}
	}
}

func TestDescribeWorkspaceExcludesCredentialLikeFilesAndDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"README.md":         "safe context",
		"client.pem":        "private certificate",
		"docs/api_token.md": "must not leave workspace",
		"secrets/brief.md":  "must not leave workspace",
		"credentials.json":  "must not leave workspace",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	context, err := DescribeWorkspace(Workspace{ID: "demo", Root: root}, []string{"readme", "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Files) != 1 || context.Files[0] != "README.md" {
		t.Fatalf("sensitive paths entered the workspace inventory: %#v", context.Files)
	}
}

func TestDescribeWorkspaceRedactsSensitiveValuesEmbeddedInEligibleFiles(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"feature_flag=true",
		"API_KEY=do-not-send-this-value",
		"AWS_SECRET_ACCESS_KEY=aws-value-must-not-leak",
		`{"client_secret":"also-not-safe"}`,
		"Authorization: Bearer never-send-this",
		"github token: ghp_123456789012345678901234567890123456",
		"postgres://agent:database-password@localhost/delivery",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	context, err := DescribeWorkspace(Workspace{ID: "demo", Root: root}, []string{"readme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Excerpts) != 1 || context.RedactedValues < 6 {
		t.Fatalf("expected redacted eligible context, got %#v", context)
	}
	got := context.Excerpts[0].Content
	for _, secret := range []string{"do-not-send-this-value", "aws-value-must-not-leak", "also-not-safe", "never-send-this", "ghp_123456789012345678901234567890123456", "database-password"} {
		if strings.Contains(got, secret) {
			t.Fatalf("embedded secret leaked to agent context: %q", secret)
		}
	}
	if !strings.Contains(got, "feature_flag=true") || !strings.Contains(got, "<redacted>") {
		t.Fatalf("safe context or redaction marker missing: %q", got)
	}
}

func TestDescribeWorkspacePrioritizesScopedSourceOverPreferredDocuments(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"README.md":                     strings.Repeat("documentation ", maxWorkspaceExcerptBytes),
		"controllers/delivery/gates.go": "package delivery\n\nfunc RequireHumanGate() {}\n",
		"controllers/catalog/read.go":   "package catalog\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	context, err := DescribeWorkspace(Workspace{ID: "demo", Root: root}, []string{"controllers/delivery"})
	if err != nil {
		t.Fatal(err)
	}
	if len(context.Excerpts) == 0 || context.Excerpts[0].Path != "controllers/delivery/gates.go" {
		t.Fatalf("scoped source must be selected before docs: %#v", context.Excerpts)
	}
	for _, excerpt := range context.Excerpts {
		if excerpt.Path == "controllers/catalog/read.go" {
			t.Fatalf("unrelated source must not be included: %#v", context.Excerpts)
		}
	}
}

func TestDescribeWorkspaceBuildsAnEvidenceBasedArchitectureMap(t *testing.T) {
	root := t.TempDir()
	for file, content := range map[string]string{
		"go.mod":                 "module example.test/delivery\n",
		"package.json":           "{\"name\":\"delivery\"}\n",
		"cmd/api/main.go":        "package main\nfunc main() {}\n",
		"src/worker/main.ts":     "export {}\n",
		"tests/delivery_test.go": "package tests\n",
		"e2e/login.spec.ts":      "export {}\n",
		"playwright.config.ts":   "export default {}\n",
		"docs/architecture.md":   "# Architecture\n",
		"credentials.json":       "must never be inventoried\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	context, err := DescribeWorkspace(Workspace{ID: "demo", Root: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(context.Architecture.RuntimeHints, []string{"go", "node"}) ||
		!reflect.DeepEqual(context.Architecture.EntrypointPaths, []string{"cmd/api/main.go", "src/worker/main.ts"}) ||
		!reflect.DeepEqual(context.Architecture.TestRoots, []string{"e2e", "playwright", "tests"}) ||
		!reflect.DeepEqual(context.Architecture.DocumentationPaths, []string{"docs/architecture.md"}) {
		t.Fatalf("unexpected architecture signals: %#v", context.Architecture)
	}
	for _, item := range append(append([]string{}, context.Architecture.EntrypointPaths...), context.Architecture.DocumentationPaths...) {
		if item == "credentials.json" {
			t.Fatal("unsafe files must not enter the architecture map")
		}
	}
}

func TestDeliveryWorkspaceContextUsesBoundedTaskFocusWhenScopeIsEmpty(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"README.md":                           "safe architecture overview",
		"controllers/delivery/human_gates.go": "package delivery\n\nfunc RequireGate() {}\n",
		"controllers/catalog/inventory.go":    "package catalog\n",
		"internal/automationagent/worker.go":  "package automationagent\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"demo":{"path":"` + filepath.ToSlash(root) + `"}}`
		}
		return ""
	}
	delivery := json.RawMessage(`{"work_item":{"title":"Delivery gates need a clearer review", "description":"", "expected_outcome":"", "included_scope":[]}, "context_sources":[{"kind":"repository","reference":"workspace://demo"}]}`)
	contexts, err := DeliveryWorkspaceContext(delivery, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(contexts) != 1 || len(contexts[0].Excerpts) == 0 || contexts[0].Excerpts[0].Path != "controllers/delivery/human_gates.go" {
		t.Fatalf("task focus must select relevant safe code first: %#v", contexts)
	}
	for _, excerpt := range contexts[0].Excerpts {
		if excerpt.Path == "controllers/catalog/inventory.go" {
			t.Fatalf("unrelated source must not enter focused context: %#v", contexts[0].Excerpts)
		}
	}
}

func TestDeliveryWorkspaceContextKeepsGitHubOnlyRepositoryAsMetadataInsteadOfFailing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("local backend context"), 0600); err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"backend":{"path":"` + filepath.ToSlash(root) + `"}}`
		}
		return ""
	}
	delivery := json.RawMessage(`{
		"context_sources":[
			{"kind":"repository","reference":"workspace://backend"},
			{"kind":"repository","reference":"github://Itbem-Corp/dashboard"}
		],
		"repository_topology":[
			{"name":"Backend","reference":"workspace://backend","revision":"aaa","role":"primary"},
			{"name":"Dashboard","reference":"github://Itbem-Corp/dashboard","revision":"bbb","role":"supporting"}
		]
	}`)
	workspaces, err := DeliveryWorkspaceContext(delivery, lookup)
	if err != nil || len(workspaces) != 1 || workspaces[0].WorkspaceID != "backend" {
		t.Fatalf("github-only frozen context must not require a local checkout: %#v / %v", workspaces, err)
	}
	remote, err := DeliveryRemoteRepositoryContexts(delivery)
	if err != nil || len(remote) != 1 || remote[0].Reference != "github://Itbem-Corp/dashboard" || remote[0].CodeAvailable || remote[0].Access != "metadata_only" {
		t.Fatalf("remote source must be visible only as metadata: %#v / %v", remote, err)
	}
}

func TestDeliveryRemoteRepositoryContextDeclaresBoundedFrozenSourceWhenPresent(t *testing.T) {
	delivery := json.RawMessage(`{
		"context_sources":[{"kind":"repository","reference":"github://Itbem-Corp/dashboard","metadata":{"github_context_mode":"bounded_source","github_code_context":{"revision":"abc"}}}],
		"repository_topology":[{"name":"Dashboard","reference":"github://Itbem-Corp/dashboard","revision":"abc","role":"supporting"}]
	}`)
	remote, err := DeliveryRemoteRepositoryContexts(delivery)
	if err != nil || len(remote) != 1 || !remote[0].CodeAvailable || remote[0].Access != "bounded_source_excerpt" {
		t.Fatalf("remote context must disclose its limited frozen source access: %#v / %v", remote, err)
	}
}

func TestDeliveryWorkspaceContextRefusesDirtyCheckoutBeforeProviderCall(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("clean base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git baseline failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("uncommitted state\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"demo":{"path":"` + filepath.ToSlash(root) + `"}}`
		}
		return ""
	}
	delivery := json.RawMessage(`{"work_item":{"title":"Review context"},"context_sources":[{"kind":"repository","reference":"workspace://demo"}]}`)
	if _, err := DeliveryWorkspaceContext(delivery, lookup); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty checkout must stop before provider context is built: %v", err)
	}
}

func TestDeliveryWorkspaceContextRefusesMismatchedFrozenRevision(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("clean base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git baseline failed: %#v / %v", result, err)
		}
	}
	lookup := func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"demo":{"path":"` + filepath.ToSlash(root) + `"}}`
		}
		return ""
	}
	delivery := json.RawMessage(`{"work_item":{"title":"Review context"},"context_sources":[{"kind":"repository","reference":"workspace://demo","revision":"` + strings.Repeat("a", 40) + `"}]}`)
	if _, err := DeliveryWorkspaceContext(delivery, lookup); err == nil || !strings.Contains(err.Error(), "frozen context revision") {
		t.Fatalf("mismatched frozen revision must stop before provider context is built: %v", err)
	}
}

func TestWorkspaceGitStateIsSanitizedAndDetectsLocalChanges(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}, {"git", "remote", "add", "origin", "https://github.com/Itbem-Corp/itbem-events-backend.git"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	state := workspaceGitState(root)
	if !state.Available || len(state.HeadSHA) != 40 || !state.HasLocalChanges || state.LocalChangeCount != 1 || state.GitHubRepository != "Itbem-Corp/itbem-events-backend" {
		t.Fatalf("unexpected safe Git state: %#v", state)
	}
}

func TestFetchWorkspaceRemoteUpdatesRefsWithoutChangingCheckout(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "origin.git")
	root := t.TempDir()
	for _, setup := range [][]string{{"git", "init", "--bare", remote}, {"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		directory := root
		if setup[1] == "init" && len(setup) == 4 && setup[2] == "--bare" {
			directory = filepath.Dir(remote)
		}
		result, err := runLocal(context.Background(), directory, commandTimeout, "", setup[0], setup[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}, {"git", "remote", "add", "origin", remote}, {"git", "push", "origin", "HEAD:main"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	before := workspaceGitState(root)
	workspace := Workspace{ID: "demo", Root: root, Config: WorkspaceConfig{Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityFetchRemote}}}
	after, err := FetchWorkspaceRemote(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Available || after.HeadSHA != before.HeadSHA || after.HasLocalChanges || after.Branch != before.Branch {
		t.Fatalf("fetch must not change the checked-out workspace: before=%#v after=%#v", before, after)
	}
	readOnly := Workspace{ID: "read-only", Root: root, Config: WorkspaceConfig{Capabilities: []string{WorkspaceCapabilityReadRepository}}}
	if _, err := FetchWorkspaceRemote(context.Background(), readOnly); err == nil {
		t.Fatal("remote fetch must require an explicit workspace capability")
	}
}

func TestSyncManagedWorkspaceClonesFastForwardsAndRejectsDirtyCheckout(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "origin.git")
	seed := t.TempDir()
	for _, command := range [][]string{{"git", "init", "--bare", remote}, {"git", "init", "-b", "main"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), seed, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}, {"git", "remote", "add", "origin", remote}, {"git", "push", "-u", "origin", "main"}} {
		result, err := runLocal(context.Background(), seed, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("seed push failed: %#v / %v", result, err)
		}
	}
	root := filepath.Join(t.TempDir(), "managed", "project")
	workspace := Workspace{ID: "managed", Root: root, Config: WorkspaceConfig{RepositoryURL: remote, BaseBranch: "main", Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityFetchRemote}}}
	state, err := SyncManagedWorkspace(context.Background(), workspace)
	if err != nil || !state.Available || state.Branch != "main" || state.HasLocalChanges {
		t.Fatalf("managed clone was not ready: %#v / %v", state, err)
	}
	firstSHA := state.HeadSHA
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "advance"}, {"git", "push", "origin", "main"}} {
		result, runErr := runLocal(context.Background(), seed, commandTimeout, "", command[0], command[1:]...)
		if runErr != nil || result.ExitCode != 0 {
			t.Fatalf("seed advance failed: %#v / %v", result, runErr)
		}
	}
	state, err = SyncManagedWorkspace(context.Background(), workspace)
	if err != nil || state.HeadSHA == firstSHA || state.Branch != "main" {
		t.Fatalf("managed checkout did not fast-forward: %#v / %v", state, err)
	}
	if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("do not overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncManagedWorkspace(context.Background(), workspace); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty managed checkout must not be switched: %v", err)
	}
}

func TestSyncManagedWorkspaceRejectsOriginThatDiffersFromRegisteredRemote(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init", "-b", "main"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("clean\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}, {"git", "remote", "add", "origin", "https://example.invalid/untrusted.git"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	workspace := Workspace{ID: "managed", Root: root, Config: WorkspaceConfig{RepositoryURL: "https://example.invalid/registered.git", BaseBranch: "main", Capabilities: []string{WorkspaceCapabilityReadRepository, WorkspaceCapabilityFetchRemote}}}
	if _, err := SyncManagedWorkspace(context.Background(), workspace); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("managed sync must reject a redirected origin before fetching: %v", err)
	}
	if !sameManagedRemote("https://example.invalid/registered.git/", "https://example.invalid/registered.git") || sameManagedRemote("https://example.invalid/other.git", "https://example.invalid/registered.git") {
		t.Fatal("managed remote comparison must allow only cosmetic trailing slashes")
	}
}

func TestDiagnoseWorkspacesReportsReadinessWithoutSourceOrPathDisclosure(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	diagnostics, err := DiagnoseWorkspaces(func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"zeta":{"path":"` + filepath.ToSlash(root) + `","validation_commands":[["go","test","./..."]]},"alpha":{"path":"` + filepath.ToSlash(root) + `"}}`
		}
		return ""
	})
	if err != nil {
		t.Fatalf("diagnose workspaces: %v", err)
	}
	if len(diagnostics) != 2 || diagnostics[0].ID != "alpha" || diagnostics[1].ID != "zeta" {
		t.Fatalf("workspace diagnostics must be stable: %#v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if !diagnostic.Ready || !diagnostic.Git.Available || diagnostic.Issue != "" {
			t.Fatalf("expected ready Git workspace: %#v", diagnostic)
		}
		if diagnostic.ScreenshotMode != "default_responsive" {
			t.Fatalf("expected default responsive screenshot harness: %#v", diagnostic)
		}
		if diagnostic.SemanticQAMode != "disabled" {
			t.Fatalf("unexpected semantic QA mode for an unconfigured workspace: %#v", diagnostic)
		}
		if diagnostic.ID == "zeta" && diagnostic.ValidationCommandCount != 1 {
			t.Fatalf("command counts missing from diagnostic: %#v", diagnostic)
		}
	}
}

func TestWorkspaceReadinessSnapshotIsSafeAndRepresentsQAAndPublicationCapabilities(t *testing.T) {
	root := t.TempDir()
	for _, command := range [][]string{{"git", "init"}, {"git", "config", "user.email", "test@example.invalid"}, {"git", "config", "user.name", "ITBEM Test"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"git", "add", "README.md"}, {"git", "commit", "-m", "initial"}} {
		result, err := runLocal(context.Background(), root, commandTimeout, "", command[0], command[1:]...)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("git setup failed: %#v / %v", result, err)
		}
	}
	readiness, err := WorkspaceReadinessSnapshot(func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"dashboard":{"path":"` + filepath.ToSlash(root) + `","capabilities":["repository:read","worktree:create","patch:apply","commit:stage","branch:publish","pull_request:create"],"validation_commands":[["go","test","./..."]],"qa_commands":[["go","test","./..."]],"qa_semantic_command":["node","runner.mjs","--url","{preview_url}","--output","{artifact_path}","--plan","{qa_plan_path}"]}}`
		}
		return ""
	})
	if err != nil || len(readiness) != 1 {
		t.Fatalf("workspace readiness: %#v / %v", readiness, err)
	}
	entry := readiness[0]
	if entry.ID != "dashboard" || !entry.Ready || !entry.QAReady || !entry.VisualQAReady || !entry.PublicationReady || entry.ValidationCommandCount != 1 || entry.QACommandCount != 1 {
		t.Fatalf("unexpected readiness projection: %#v", entry)
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{filepath.ToSlash(root), "README.md", "head_sha", "branch", "capabilities"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("readiness leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestParseGitAheadBehindRejectsMalformedOutput(t *testing.T) {
	if ahead, behind := parseGitAheadBehind("3 7\n"); ahead != 3 || behind != 7 {
		t.Fatalf("unexpected ahead/behind parse: %d/%d", ahead, behind)
	}
	for _, raw := range []string{"", "3", "3 nope", "-1 2", "1 2 3"} {
		if ahead, behind := parseGitAheadBehind(raw); ahead != 0 || behind != 0 {
			t.Fatalf("unsafe ahead/behind parse for %q: %d/%d", raw, ahead, behind)
		}
	}
}

func TestWorkspaceRejectsShellCommandsAndUnsafeScreenshotTemplates(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","qa_commands":[["powershell","-Command","whoami"]]}}`); err == nil {
		t.Fatal("expected shell command rejection")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","qa_screenshot_command":["npx","shot","{preview_url}"]}}`); err == nil {
		t.Fatal("expected incomplete screenshot template rejection")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","qa_semantic_command":["powershell","-Command","whoami"]}}`); err == nil {
		t.Fatal("expected semantic shell command rejection")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","qa_semantic_command":["node","runner.mjs","--url","{preview_url}","--output","{artifact_path}","--plan","{qa_plan_path}"]}}`); err != nil {
		t.Fatalf("a bounded private QA plan path should be accepted: %v", err)
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","qa_semantic_command":["C:/managed/node.exe","runner.mjs","--url","{preview_url}","--output","{artifact_path}","--plan","{qa_plan_path}"]}}`); err != nil {
		t.Fatalf("an absolute managed Node runtime should be accepted for Stagehand: %v", err)
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","qa_semantic_command":["node","runner.mjs","--url","{preview_url}","--output","{artifact_path}","--plan","{qa_plan_path}","--again","{qa_plan_path}"]}}`); err == nil {
		t.Fatal("duplicate QA plan placeholders must be rejected")
	}
}

func TestWorkspaceCapabilitiesAreBoundedAndPublishIsNeverImplicit(t *testing.T) {
	root := t.TempDir()
	raw := `{"demo":{"path":"` + filepath.ToSlash(root) + `","capabilities":["repository:read","worktree:create","patch:apply"]}}`
	workspaces, err := LoadWorkspaces(raw)
	if err != nil {
		t.Fatalf("load workspace: %v", err)
	}
	workspace := workspaces["demo"]
	if !workspace.AllowsCapability(WorkspaceCapabilityApplyPatch) {
		t.Fatal("explicit patch capability was not retained")
	}
	if workspace.AllowsCapability(WorkspaceCapabilityPublishBranch) {
		t.Fatal("branch publication must never be implicit")
	}
	if err := workspace.RequireCapability(WorkspaceCapabilityPublishBranch); err == nil {
		t.Fatal("expected missing publication capability to be rejected")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","capabilities":["repository:read","shell:execute"]}}`); err == nil {
		t.Fatal("expected unsupported workspace capability to be rejected")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","capabilities":["repository:read","pull_request:create"]}}`); err == nil {
		t.Fatal("pull request capability without branch publication must be rejected")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","capabilities":["repository:read","branch:publish"]}}`); err == nil {
		t.Fatal("branch publication without staging capability must be rejected")
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","capabilities":["repository:read","commit:stage","branch:publish","pull_request:create"]}}`); err != nil {
		t.Fatalf("complete publication chain should remain configurable: %v", err)
	}
}

func TestWorkspaceContextExposesOnlySafeHarnessCapabilities(t *testing.T) {
	root := t.TempDir()
	workspace, err := RegisteredWorkspace("workspace://demo", func(key string) string {
		if key == "ITBEM_AI_WORKSPACES_JSON" {
			return `{"demo":{"path":"` + filepath.ToSlash(root) + `","validation_commands":[["go","test","./..."]],"qa_commands":[["npm","run","test:e2e"]],"qa_artifact_patterns":["test-results/*.xml"],"qa_screenshot_command":["npx","capture","{preview_url}","{artifact_path}"],"qa_semantic_command":["node","tools/stagehand-qa/run.mjs","--url","{preview_url}","--output","{artifact_path}"]}}`
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	context, err := DescribeWorkspace(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if context.Harness.ValidationCommandCount != 1 || context.Harness.QACommandCount != 1 || !context.Harness.ArtifactCollection || context.Harness.ScreenshotMode != "configured_command" || context.Harness.SemanticQAMode != "configured_command" {
		t.Fatalf("safe harness profile is incomplete: %#v", context.Harness)
	}
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "test:e2e") || strings.Contains(string(encoded), "capture") {
		t.Fatalf("workspace context must not expose harness command arguments: %s", encoded)
	}
	if _, err := LoadWorkspaces(`{"demo":{"path":"` + filepath.ToSlash(root) + `","validation_commands":[["go","test","--api-token=must-not-be-accepted"]]}}`); err == nil {
		t.Fatal("secret-shaped harness command argument must be rejected")
	}
}

func TestDefaultScreenshotCommandUsesExplicitHeadlessFlags(t *testing.T) {
	// This test intentionally validates the browser invocation shape without
	// requiring a browser binary in CI.
	command, err := defaultScreenshotCommand("http://127.0.0.1:3000", filepath.Join(t.TempDir(), "preview.png"))
	if err != nil {
		t.Skip("local Chromium is not installed")
	}
	joined := strings.Join(command, " ")
	for _, expected := range []string{"--headless=new", "--disable-gpu-shader-disk-cache", "--disable-gpu-program-cache", "--window-size=1440,1200", "--user-data-dir=", "--screenshot="} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing screenshot harness flag %q in %v", expected, command)
		}
	}
}

func TestDefaultScreenshotCommandRejectsUnsafeViewportBeforeLookingForBrowser(t *testing.T) {
	if _, err := defaultScreenshotCommandAt("https://preview.example.test", filepath.Join(t.TempDir(), "preview.png"), screenshotViewport{Name: "bad", Width: 1, Height: 1}); err == nil {
		t.Fatal("unsafe screenshot viewport must be rejected")
	}
}

func TestCollectQAArtifactsAcceptsOnlyBoundedEvidenceTypes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.exe"), []byte("no"), 0600); err != nil {
		t.Fatal(err)
	}
	artifacts, err := collectQAArtifacts(root, []string{"*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "evidence.txt" {
		t.Fatalf("unexpected QA artifacts: %#v", artifacts)
	}
}

func TestReadSafeWorkspaceArtifactRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "evidence.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks unavailable in this environment")
	}
	if _, err := readSafeWorkspaceArtifact(root, link); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
