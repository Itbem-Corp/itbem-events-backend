//go:build integration

package automation

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentcallbackauth"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWorkspaceCatalogSignedPostgresRegistrationAndRevocation(t *testing.T) {
	ctx := context.Background()
	container, err := postgrescontainer.Run(ctx, "postgres:16-alpine", postgrescontainer.WithDatabase("testdb"), postgrescontainer.WithUsername("test"), postgrescontainer.WithPassword("test"), postgrescontainer.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error)
	require.NoError(t, configuration.MigrateModelsForTest(db))
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })
	client := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "catalog synthetic", Code: "catalog-synthetic", ClientTypeID: uuid.Must(uuid.NewV4()), IsActive: true}
	require.NoError(t, db.Create(&client).Error)
	project := models.DeliveryProject{ID: uuid.Must(uuid.NewV4()), ClientID: client.ID, Name: "catalog synthetic", Slug: "catalog-synthetic", Status: "active", CreatedBy: "synthetic-owner"}
	require.NoError(t, db.Create(&project).Error)
	source := models.DeliveryContextSource{ID: uuid.Must(uuid.NewV4()), ProjectID: project.ID, Kind: "repository", Name: "API", Reference: "github://itbem-corp/api", Revision: strings.Repeat("a", 40), Status: "ready", MetadataJSON: `{"github_default_branch":"main","allowed_paths":["src"]}`}
	require.NoError(t, db.Create(&source).Error)
	machine, err := automationagent.LoadLocalMachineIdentity("", t.TempDir())
	require.NoError(t, err)
	public, err := agentcallbackauth.EncodePublicKey(machine.PublicKey())
	require.NoError(t, err)
	instance := models.AutomationAgentInstance{ID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: machine.MachineID(), PublicKey: public, Status: "active"}
	require.NoError(t, db.Create(&instance).Error)
	policy, err := json.Marshal(map[string]any{"version": 1, "scopes": []workspaceCatalogScope{{AgentKey: "generalist", MachineIDs: []string{machine.MachineID()}, ClientIDs: []string{client.ID.String()}, Roles: []string{"principal_engineer"}, RepositoryOwners: []string{"itbem-corp"}, DefaultProfile: "go-v1", AllowedProfiles: []string{"go-v1"}}}})
	require.NoError(t, err)
	t.Setenv("AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON", string(policy))
	t.Setenv("AUTOMATION_CALLBACK_SECRET", "catalog-integration-test-root")
	e := echo.New()
	e.Use(AgentCallbackAuthentication)
	e.GET("/api/internal/automation/workspace-catalog", WorkspaceCatalog)
	e.POST("/api/internal/automation/workspace-catalog/ready", WorkspaceCatalogReady)
	server := httptest.NewTLSServer(e)
	defer server.Close()
	callback, err := automationagent.NewHTTPCallback(server.URL, machine, instance.ID.String(), server.Client())
	require.NoError(t, err)
	config := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}, GatewayToken: deriveGatewayToken("catalog-integration-test-root", gatewayIdentity{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering})}
	catalog, err := callback.FetchWorkspaceCatalog(ctx, config)
	require.NoError(t, err)
	require.Len(t, catalog.Entries, 1)
	entry := catalog.Entries[0]
	ready := automationagent.WorkspaceCatalogReadyRequest{Digest: catalog.Digest, Ready: []automationagent.CatalogReadyWorkspace{{ID: entry.ID, Revision: entry.Revision, Readiness: automationagent.WorkspaceReadiness{ID: entry.ID, Ready: true, SandboxReady: true, IsolationMode: "docker_container"}, Capabilities: []string{"repository:read", "repository:fetch", "worktree:create", "patch:apply"}}}}
	for _, mode := range []string{"docker", "firecracker", "host_process", ""} {
		invalid := ready
		invalid.Ready = append([]automationagent.CatalogReadyWorkspace(nil), ready.Ready...)
		invalid.Ready[0].Readiness.IsolationMode = mode
		require.Error(t, callback.ReportWorkspaceCatalogReady(ctx, config, invalid), "unsupported isolation mode %q registered", mode)
	}
	require.NoError(t, callback.ReportWorkspaceCatalogReady(ctx, config, ready))
	require.NoError(t, callback.ReportWorkspaceCatalogReady(ctx, config, ready))
	var count int64
	require.NoError(t, db.Model(&models.DeliveryContextSource{}).Where("project_id = ? AND reference = ?", project.ID, "workspace://"+entry.ID).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var registered models.DeliveryContextSource
	require.NoError(t, db.Where("reference = ?", "workspace://"+entry.ID).First(&registered).Error)
	require.Contains(t, registered.MetadataJSON, `"allowed_paths": ["src"]`)
	require.NoError(t, db.Model(&source).Update("revision", strings.Repeat("b", 40)).Error)
	require.Error(t, callback.ReportWorkspaceCatalogReady(ctx, config, ready), "stale frozen context registered")
	var unchanged models.DeliveryContextSource
	require.NoError(t, db.First(&unchanged, registered.ID).Error)
	require.Equal(t, entry.Revision, unchanged.Revision)
	t.Setenv("AUTOMATION_WORKSPACE_CATALOG_POLICY_JSON", `{"version":1,"scopes":[]}`)
	_, err = callback.FetchWorkspaceCatalog(ctx, config)
	require.Error(t, err)
	require.Error(t, callback.ReportWorkspaceCatalogReady(ctx, config, ready), "revoked machine registered new source")
	var calls int64
	require.NoError(t, db.Model(&models.AutomationExecution{}).Where("created_at > ?", time.Now().Add(-time.Hour)).Count(&calls).Error)
	require.Zero(t, calls, "catalog called a model")
}
