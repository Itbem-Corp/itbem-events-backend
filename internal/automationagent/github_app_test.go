package automationagent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestLoadGitHubAppConfigFailsClosedWhenIncomplete(t *testing.T) {
	_, err := LoadGitHubAppConfig(func(string) string { return "" })
	if !errors.Is(err, ErrGitHubAppNotConfigured) {
		t.Fatalf("expected missing App credentials to remain local-only, got %v", err)
	}
}

func TestLoadGitHubAppConfigAcceptsEscapedPEMAndRejectsUnsafeEndpoint(t *testing.T) {
	key := testGitHubAppKey(t)
	pemValue := strings.ReplaceAll(testGitHubAppPEM(t, key), "\n", `\n`)
	values := map[string]string{
		"ITBEM_GITHUB_APP_ID":          "12345",
		"ITBEM_GITHUB_INSTALLATION_ID": "67890",
		"ITBEM_GITHUB_APP_PRIVATE_KEY": pemValue,
		"ITBEM_GITHUB_API_BASE_URL":    "https://github.example.test/api/v3",
	}
	config, err := LoadGitHubAppConfig(func(name string) string { return values[name] })
	if err != nil || config.PrivateKey == nil || config.APIBaseURL != "https://github.example.test/api/v3" {
		t.Fatalf("expected valid GitHub App configuration, got %#v / %v", config, err)
	}
	values["ITBEM_GITHUB_API_BASE_URL"] = "http://github.example.test"
	if _, err := LoadGitHubAppConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected non-loopback HTTP GitHub endpoint to be rejected")
	}
}

func TestGitHubAppConfigAllowsOnlyExplicitInstallationIDs(t *testing.T) {
	key := testGitHubAppKey(t)
	values := map[string]string{
		"ITBEM_GITHUB_APP_ID":           "12345",
		"ITBEM_GITHUB_INSTALLATION_IDS": "157019146, 157018577,157019259,157018577",
		"ITBEM_GITHUB_APP_PRIVATE_KEY":  testGitHubAppPEM(t, key),
	}
	config, err := LoadGitHubAppConfig(func(name string) string { return values[name] })
	if err != nil || len(config.InstallationIDs) != 3 || !config.AllowsInstallationID(157018577) || config.AllowsInstallationID(1) {
		t.Fatalf("expected an explicit multi-installation allow-list, got %#v / %v", config.InstallationIDs, err)
	}
	selected, err := config.WithInstallationID(157019259)
	if err != nil || selected.InstallationID != "157019259" || len(selected.InstallationIDs) != 3 {
		t.Fatalf("expected selected installation token config, got %#v / %v", selected, err)
	}
	if _, err := config.WithInstallationID(1); err == nil {
		t.Fatal("unconfigured GitHub App installation must be rejected")
	}
}

func TestReadGitHubPullRequestPatchPinsWebhookCommitComparison(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/repos/itbem/backend/compare/"+base+"..."+head {
			t.Fatalf("patch must use exact webhook commit comparison, got %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer ephemeral" || request.Header.Get("Accept") != "application/vnd.github.v3.diff" {
			t.Fatal("patch request must use only the installation token and diff media type")
		}
		_, _ = response.Write([]byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"))
	}))
	defer server.Close()
	patch, err := ReadGitHubPullRequestPatch(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral", "itbem/backend", base, head)
	if err != nil || !strings.Contains(patch, "+new") {
		t.Fatalf("read immutable patch: %q / %v", patch, err)
	}
	if _, err := ReadGitHubPullRequestPatch(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral", "itbem/backend", base, base); err == nil {
		t.Fatal("same base/head SHA must not create a review comparison")
	}
}

func TestReadGitHubPullRequestStateReturnsOnlyAValidOpenCurrentPR(t *testing.T) {
	head := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/repos/itbem/backend/pulls/42" || request.Header.Get("Authorization") != "Bearer ephemeral" {
			t.Fatalf("unexpected current-head request: %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"state": "open", "draft": false, "merged": false, "head": map[string]string{"sha": head}})
	}))
	defer server.Close()
	actual, err := ReadGitHubPullRequestState(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral", "itbem/backend", 42)
	if err != nil || actual.HeadSHA != head || !actual.Open || actual.Draft || actual.Merged {
		t.Fatalf("read current PR state: %#v / %v", actual, err)
	}
	if _, err := ReadGitHubPullRequestState(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral", "itbem/backend", 0); err == nil {
		t.Fatal("invalid PR number must fail before network access")
	}
}

func TestGitHubPullRequestStateKeepsClosedDraftOrMergedDeliveryOutOfReview(t *testing.T) {
	head := strings.Repeat("b", 40)
	for name, payload := range map[string]map[string]any{
		"closed": {"state": "closed", "head": map[string]string{"sha": head}},
		"draft":  {"state": "open", "draft": true, "head": map[string]string{"sha": head}},
		"merged": {"state": "closed", "merged": true, "head": map[string]string{"sha": head}},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				_ = json.NewEncoder(response).Encode(payload)
			}))
			defer server.Close()
			state, err := ReadGitHubPullRequestState(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral", "itbem/backend", 42)
			if err != nil || state.HeadSHA != head || (state.Open && !state.Draft && !state.Merged) {
				t.Fatalf("obsolete PR state must remain distinguishable: %#v / %v", state, err)
			}
		})
	}
}

func TestMintGitHubInstallationTokenUsesSignedAppJWT(t *testing.T) {
	key := testGitHubAppKey(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/app/installations/67890/access_tokens" {
			t.Fatalf("unexpected GitHub App endpoint: %s %s", request.Method, request.URL.Path)
		}
		authorization := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		token, err := jwt.Parse(authorization, func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodRS256 {
				t.Fatalf("unexpected GitHub App JWT method: %s", token.Method.Alg())
			}
			return &key.PublicKey, nil
		}, jwt.WithTimeFunc(func() time.Time { return now }))
		if err != nil || !token.Valid {
			t.Fatalf("GitHub App assertion is invalid: %v", err)
		}
		claims := token.Claims.(jwt.MapClaims)
		if claims["iss"] != "12345" {
			t.Fatalf("unexpected GitHub App issuer: %#v", claims)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(map[string]string{"token": "installation-token-not-persisted", "expires_at": now.Add(45 * time.Minute).Format(time.RFC3339)})
	}))
	defer server.Close()

	installation, err := MintGitHubInstallationToken(context.Background(), GitHubAppConfig{AppID: "12345", InstallationID: "67890", PrivateKey: key, APIBaseURL: server.URL}, server.Client(), now)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	if installation.Token != "installation-token-not-persisted" || !installation.ExpiresAt.After(now) {
		t.Fatalf("unexpected installation token metadata: %#v", installation)
	}
}

func TestVerifyGitHubAppInstallationUsesEphemeralTokenAndDoesNotExposeRepositories(t *testing.T) {
	key := testGitHubAppKey(t)
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/67890/access_tokens":
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]string{"token": "ephemeral-installation-token", "expires_at": now.Add(45 * time.Minute).Format(time.RFC3339)})
		case request.Method == http.MethodGet && request.URL.Path == "/installation/repositories":
			if request.Header.Get("Authorization") != "Bearer ephemeral-installation-token" {
				t.Fatal("verification must use only the ephemeral installation token")
			}
			if request.URL.Query().Get("per_page") != "1" {
				t.Fatal("verification must minimize repository response size")
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"total_count": 2, "repositories": []map[string]string{{"full_name": "private/repository"}}})
		default:
			t.Fatalf("unexpected GitHub verification request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	err := VerifyGitHubAppInstallation(context.Background(), GitHubAppConfig{AppID: "12345", InstallationID: "67890", PrivateKey: key, APIBaseURL: server.URL}, server.Client(), now)
	if err != nil {
		t.Fatalf("verify installation: %v", err)
	}
}

func TestCreateGitHubPullRequestIsScopedAndRetrySafe(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer ephemeral-installation-token" {
			t.Fatal("installation token must stay in an authorization header")
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/Itbem-Corp/repo":
			_ = json.NewEncoder(response).Encode(map[string]string{"default_branch": "main"})
		case request.Method == http.MethodPost && request.URL.Path == "/repos/Itbem-Corp/repo/pulls":
			requests++
			var payload map[string]string
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload["head"] != "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb" || payload["base"] != "main" {
				t.Fatalf("unexpected PR scope: %#v", payload)
			}
			response.WriteHeader(http.StatusUnprocessableEntity)
		case request.Method == http.MethodGet && request.URL.Path == "/repos/Itbem-Corp/repo/pulls":
			if request.URL.Query().Get("head") != "Itbem-Corp:itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb" {
				t.Fatalf("unexpected PR lookup: %s", request.URL.String())
			}
			_ = json.NewEncoder(response).Encode([]map[string]string{{"html_url": "https://github.com/Itbem-Corp/repo/pull/7"}})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	url, created, err := createGitHubPullRequest(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral-installation-token", githubRepository{Owner: "Itbem-Corp", Name: "repo"}, "itbem-agent/d4a4b837-2e18-43af-9f58-6d59629db2bb", "Approved change")
	if err != nil || created || url != "https://github.com/Itbem-Corp/repo/pull/7" || requests != 1 {
		t.Fatalf("expected existing PR recovery: %q %v %v", url, created, err)
	}
}

func TestReadGitHubRepositorySnapshotUsesOnlyRegisteredReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer ephemeral-installation-token" {
			t.Fatal("installation token must only be sent as an authorization header")
		}
		switch request.URL.Path {
		case "/repos/Itbem-Corp/repo":
			_ = json.NewEncoder(response).Encode(map[string]string{"default_branch": "main"})
		case "/repos/Itbem-Corp/repo/commits/main":
			_ = json.NewEncoder(response).Encode(map[string]string{"sha": strings.Repeat("a", 40)})
		default:
			t.Fatalf("unexpected remote read: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	snapshot, err := ReadGitHubRepositorySnapshot(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral-installation-token", "github://Itbem-Corp/repo")
	if err != nil || snapshot.Reference != "github://Itbem-Corp/repo" || snapshot.DefaultBranch != "main" || snapshot.Revision != strings.Repeat("a", 40) {
		t.Fatalf("unexpected remote snapshot: %#v / %v", snapshot, err)
	}
	if _, err := ReadGitHubRepositorySnapshot(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral-installation-token", "git@github.com:Itbem-Corp/repo.git"); err == nil {
		t.Fatal("read API must reject an unregistered or ambiguous remote reference")
	}
}

func TestReadGitHubRepositoryMapOnlyReturnsSafeFileInventory(t *testing.T) {
	revision := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer ephemeral-installation-token" {
			t.Fatal("repository map must use only the ephemeral installation token")
		}
		if request.Method != http.MethodGet || request.URL.Path != "/repos/Itbem-Corp/repo/git/trees/"+revision || request.URL.Query().Get("recursive") != "1" {
			t.Fatalf("unexpected repository map request: %s", request.URL.String())
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"tree": []map[string]string{
			{"path": "README.md", "type": "blob"},
			{"path": "cmd/api/main.go", "type": "blob"},
			{"path": ".env.production", "type": "blob"},
			{"path": "node_modules/ignored.js", "type": "blob"},
			{"path": "secrets/key.txt", "type": "blob"},
			{"path": "docs", "type": "tree"},
		}})
	}))
	defer server.Close()

	result, err := ReadGitHubRepositoryMap(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral-installation-token", GitHubRepositorySnapshot{Reference: "github://Itbem-Corp/repo", Revision: revision})
	if err != nil {
		t.Fatalf("read repository map: %v", err)
	}
	if result.Revision != revision || result.FileCount != 2 || result.InventoryTruncated || strings.Join(result.Files, ",") != "README.md,cmd/api/main.go" {
		t.Fatalf("unexpected safe repository map: %#v", result)
	}
}

func TestReadGitHubRepositorySourceContextUsesOnlySelectedRedactedFrozenFiles(t *testing.T) {
	revision := strings.Repeat("c", 40)
	content := map[string]string{
		"README.md":            "# Delivery\nAPI_KEY=must-not-reach-a-model\n",
		"package.json":         `{"name":"delivery"}`,
		"src/main.ts":          "export const entrypoint = true\n",
		"docs/architecture.md": "# Architecture\n",
	}
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer ephemeral-installation-token" || request.Method != http.MethodGet || request.URL.Query().Get("ref") != revision {
			t.Fatalf("unexpected source request: %s %s", request.Method, request.URL.String())
		}
		path := strings.TrimPrefix(request.URL.Path, "/repos/Itbem-Corp/repo/contents/")
		body, exists := content[path]
		if !exists {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		seen[path] = true
		_ = json.NewEncoder(response).Encode(map[string]any{"type": "file", "encoding": "base64", "size": len(body), "content": base64.StdEncoding.EncodeToString([]byte(body))})
	}))
	defer server.Close()

	source, err := ReadGitHubRepositorySourceContext(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral-installation-token", GitHubRepositorySnapshot{Reference: "github://Itbem-Corp/repo", Revision: revision}, GitHubRepositoryMap{Revision: revision, Files: []string{"README.md", "package.json", "src/main.ts", "docs/architecture.md", ".env", "secrets/key.txt"}})
	if err != nil {
		t.Fatalf("read bounded remote source context: %v", err)
	}
	encoded, marshalErr := json.Marshal(source)
	if marshalErr != nil || len(source.Excerpts) != 4 || !seen["README.md"] || source.RedactedValues == 0 || strings.Contains(string(encoded), "must-not-reach-a-model") {
		t.Fatalf("remote context must contain selected redacted source only: %#v / %#v / %v", source, seen, marshalErr)
	}
	if _, err := ReadGitHubRepositorySourceContext(context.Background(), GitHubAppConfig{APIBaseURL: server.URL}, "ephemeral-installation-token", GitHubRepositorySnapshot{Reference: "github://Itbem-Corp/repo", Revision: revision}, GitHubRepositoryMap{Revision: strings.Repeat("d", 40)}); err == nil {
		t.Fatal("remote source context must reject an inventory from another revision")
	}
}

func TestEscapeGitHubRepositoryContentPathPreservesValidatedDirectories(t *testing.T) {
	if value := escapeGitHubRepositoryContentPath("docs/a file.md"); value != "docs/a%20file.md" {
		t.Fatalf("nested GitHub contents path was escaped incorrectly: %q", value)
	}
}

func testGitHubAppKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testGitHubAppPEM(t *testing.T, key *rsa.PrivateKey) string {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
}
