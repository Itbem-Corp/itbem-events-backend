package automation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestWorkspaceCatalogScopesProjectsRepositoriesAndProfiles(t *testing.T) {
	db, mock := automationCostLedgerTestDB(t)
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	machine, client, project, source := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())
	policy, err := json.Marshal(map[string]any{"version": 1, "scopes": []workspaceCatalogScope{{AgentKey: "generalist", MachineIDs: []string{machine.String()}, ClientIDs: []string{client.String()}, Roles: []string{"principal_engineer"}, RepositoryOwners: []string{"itbem-corp"}, DefaultProfile: "go-v1", AllowedProfiles: []string{"go-v1", "node-v1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON", string(policy))
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "catalog-test-root")
	rows := sqlmock.NewRows([]string{"id", "project_id", "reference", "revision", "metadata_json"}).
		AddRow(source, project, "github://itbem-corp/api", strings.Repeat("a", 40), `{"github_default_branch":"main"}`).
		AddRow(uuid.Must(uuid.NewV4()), project, "github://other-org/private", strings.Repeat("a", 40), `{"github_default_branch":"main"}`).
		AddRow(uuid.Must(uuid.NewV4()), project, "github://itbem-corp/unsafe", strings.Repeat("a", 40), `{"github_default_branch":"main","workspace_profile":"unapproved-v1"}`)
	mock.ExpectQuery(`SELECT source\.\* FROM delivery_context_sources AS source JOIN delivery_projects AS project.*project.client_id IN.*project.deleted_at IS NULL.*client.deleted_at IS NULL`).WithArgs(client.String(), "active", true, "repository", "ready", "github://%", 257).WillReturnRows(rows)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/internal/automation/workspace-catalog", nil)
	request.Header.Set("X-Agent-Role", "principal_engineer")
	request.Header.Set("X-Agent-Lane", "engineering")
	request.Header.Set("X-Agent-Gateway-Token", deriveGatewayToken("catalog-test-root", gatewayIdentity{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}))
	ctx := echo.New().NewContext(request, response)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: machine.String()})
	if err := WorkspaceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data automationagent.WorkspaceCatalog `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(envelope.Data.Entries) != 1 || envelope.Data.Entries[0].SourceID != source.String() {
		t.Fatalf("catalog leaked out-of-scope content: status=%d %s", response.Code, response.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCatalogRejectsUnscopedMachineBeforeDatabaseQuery(t *testing.T) {
	t.Setenv("AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON", `{"version":1,"scopes":[]}`)
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "catalog-test-root")
	db, mock := automationCostLedgerTestDB(t)
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/internal/automation/workspace-catalog", nil)
	request.Header.Set("X-Agent-Role", "principal_engineer")
	request.Header.Set("X-Agent-Lane", "engineering")
	request.Header.Set("X-Agent-Gateway-Token", deriveGatewayToken("catalog-test-root", gatewayIdentity{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}))
	ctx := echo.New().NewContext(request, response)
	ctx.Set(agentCallbackIdentityContextKey, authenticatedAgentCallback{InstanceID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: uuid.Must(uuid.NewV4()).String()})
	if err := WorkspaceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if response.Code != 403 {
		t.Fatalf("unscoped machine status=%d", response.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCatalogSelectsOnlyRevisionMatchedManifestProfiles(t *testing.T) {
	scope := workspaceCatalogScope{DefaultProfile: "combined-v1", ManifestProfiles: map[string]string{"go.mod": "go-v1", "package.json": "node-v1"}}
	for _, tc := range []struct {
		revision string
		files    []string
		want     string
	}{
		{"frozen", []string{"go.mod"}, "go-v1"},
		{"frozen", []string{"package.json"}, "node-v1"},
		{"stale", []string{"package.json"}, "combined-v1"},
		{"frozen", []string{"other/package.json"}, "combined-v1"},
		{"frozen", []string{"go.mod", "package.json"}, "combined-v1"},
	} {
		if got := catalogManifestProfile(scope, automationagent.GitHubRepositoryMap{Revision: tc.revision, Files: tc.files}, "frozen"); got != tc.want {
			t.Fatalf("profile=%q want=%q files=%v", got, tc.want, tc.files)
		}
	}
}
