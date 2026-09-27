package automation

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

func TestAgentInstanceEndpointsRequirePlatformWorkspace(t *testing.T) {
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(string) (*models.User, error) {
		return &models.User{IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
	}})
	t.Cleanup(restore)

	endpoints := []struct {
		name    string
		request func(echo.Context) error
	}{
		{name: "register", request: RegisterAgentInstance},
		{name: "list", request: ListAgentInstances},
		{name: "revoke", request: RevokeAgentInstance},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			newContext := func() (echo.Context, *httptest.ResponseRecorder) {
				recorder := httptest.NewRecorder()
				ctx := echo.New().NewContext(httptest.NewRequest(http.MethodPost, "/api/automation/agent-instances", nil), recorder)
				ctx.Set("cognito_sub", "agent-instance-workspace-test")
				return ctx, recorder
			}

			organizationContext, organizationRecorder := newContext()
			organizationContext.Set("workspace_mode", "organization")
			organizationContext.Set("organization_id", uuid.Must(uuid.NewV4()))
			if err := endpoint.request(organizationContext); err != nil {
				t.Fatal(err)
			}
			if organizationRecorder.Code != http.StatusNotFound {
				t.Fatalf("organization workspace status = %d, want %d: %s", organizationRecorder.Code, http.StatusNotFound, organizationRecorder.Body.String())
			}

			tenantContext, tenantRecorder := newContext()
			tenantContext.Set("workspace_mode", "platform")
			tenantContext.Set("tenant_code", "caffetton")
			if err := endpoint.request(tenantContext); err != nil {
				t.Fatal(err)
			}
			if tenantRecorder.Code != http.StatusForbidden {
				t.Fatalf("tenant surface status = %d, want %d: %s", tenantRecorder.Code, http.StatusForbidden, tenantRecorder.Body.String())
			}

			platformContext, platformRecorder := newContext()
			platformContext.Set("workspace_mode", "platform")
			if err := endpoint.request(platformContext); err != nil {
				t.Fatal(err)
			}
			if platformRecorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("platform root should reach DB readiness check; status = %d, want %d: %s", platformRecorder.Code, http.StatusServiceUnavailable, platformRecorder.Body.String())
			}
		})
	}
}

func TestAgentInstanceCursorScopeBindsAgentAndStatusFilters(t *testing.T) {
	createdAt := time.Date(2026, 9, 24, 12, 30, 0, 0, time.UTC)
	cursor := agentInstanceCursor{Version: 1, Scope: agentInstanceCursorScope("generalist", "active"), CreatedAt: createdAt, ID: uuid.Must(uuid.NewV4()).String()}
	encoded := encodeAgentInstanceCursor(cursor)
	if _, err := decodeAgentInstanceCursor(encoded, agentInstanceCursorScope("generalist", "active")); err != nil {
		t.Fatalf("cursor should decode under its original filters: %v", err)
	}
	for _, changedScope := range []string{
		agentInstanceCursorScope("reviewer", "active"),
		agentInstanceCursorScope("generalist", "revoked"),
		agentInstanceCursorScope("reviewer", "revoked"),
	} {
		if _, err := decodeAgentInstanceCursor(encoded, changedScope); err == nil {
			t.Fatalf("cursor was accepted after filters changed to scope %s", changedScope)
		}
	}
	if agentInstanceCursorScope(" generalist ", " active ") != agentInstanceCursorScope("generalist", "active") {
		t.Fatal("cursor scope should normalize surrounding filter whitespace")
	}
}

func TestProjectAgentInstanceOmitsLegacyMachineLabel(t *testing.T) {
	instance := models.AutomationAgentInstance{ID: uuid.Must(uuid.NewV4()), AgentKey: "generalist", MachineID: "legacy-workstation-hostname", Status: "revoked"}
	dto := projectAgentInstance(instance)
	if dto.MachineID != "" {
		t.Fatalf("invalid historical machine identity should be omitted, got %q", dto.MachineID)
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "legacy-workstation-hostname") || strings.Contains(string(encoded), `"machine_id"`) {
		t.Fatalf("historical machine label was serialized: %s", encoded)
	}
}
