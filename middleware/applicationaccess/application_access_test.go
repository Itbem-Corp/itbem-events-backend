package applicationaccess

import (
	"events-stocks/dtos"
	"events-stocks/internal/organizationcontext"
	"events-stocks/internal/products"
	"events-stocks/models"
	"events-stocks/services/applications"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequiredSurfaceCapabilitySeparatesProducts(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   string
	}{
		{method: "GET", path: "/api/session", want: ""},
		{method: "GET", path: "/api/users", want: ""},
		{method: "POST", path: "/api/users/avatar", want: ""},
		{method: "GET", path: "/api/users/all", want: "platform:users:view"},
		{method: "PUT", path: "/api/users/abc", want: "platform:users:view"},
		{method: "GET", path: "/api/clients", want: "organizations:view|members:manage|events:manage"},
		{method: "POST", path: "/api/clients", want: "organizations:manage"},
		{method: "PUT", path: "/api/clients/abc", want: "organizations:manage"},
		{method: "GET", path: "/api/clients/members", want: "members:manage|applications:manage"},
		{method: "PUT", path: "/api/clients/abc/member-applications/user", want: "members:manage|applications:manage"},
		{method: "GET", path: "/api/events", want: "events:view"},
		{method: "GET", path: "/api/moments/activity", want: "events:view"},
		{method: "GET", path: "/api/catalogs/design-templates", want: "events:view"},
		{method: "POST", path: "/api/automation/tasks", want: "automation:view|automation:manage"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			assert.Equal(t, test.want, requiredSurfaceCapability(test.method, test.path))
		})
	}
}

func TestRequiresEventOperationsIsExclusiveToTheEventDomain(t *testing.T) {
	for _, path := range []string{
		"/api/events/123/studio-workspace",
		"/api/events/123/invitations",
		"/api/invitations/123/resend",
		"/api/guests/1/rsvp-token",
		"/api/moments",
		"/api/sections/123",
		"/api/tables/123",
		"/api/resources",
		"/api/fonts/upload",
		"/api/catalogs/design-templates",
	} {
		assert.True(t, products.RequiresEventOperationsPath(path), path)
	}
	assert.False(t, products.RequiresEventOperationsPath("/api/clients"))
	assert.False(t, products.RequiresEventOperationsPath("/api/users"))
	assert.True(t, products.RequiresAutomationPath("/api/automation/tasks"))
	assert.False(t, products.RequiresAutomationPath("/api/events"))
}

func requestContextTestSession(applicationCode string, platform bool, organizations ...uuid.UUID) *applications.Session {
	access := make([]applications.OrganizationAccess, 0, len(organizations))
	for _, organizationID := range organizations {
		access = append(access, applications.OrganizationAccess{ID: organizationID})
	}
	return &applications.Session{
		Application:   models.Application{Code: applicationCode, AllowsPlatformAdmin: platform},
		User:          dtos.UserProfileResponse{IsRoot: platform},
		Organizations: access,
	}
}

func requestContextEcho(headers map[string]string) echo.Context {
	return requestContextEchoPath("/api/users", headers)
}

func requestContextEchoPath(path string, headers map[string]string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest("GET", path, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return e.NewContext(req, httptest.NewRecorder())
}

func TestValidateRequestContextRejectsCrossTenantHeader(t *testing.T) {
	c := requestContextEcho(map[string]string{HeaderApplicationCode: "eventiapp"})
	err := validateRequestContext(c, "itbem", requestContextTestSession("itbem", true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestValidateRequestContextKeepsSessionBootstrapBackwardCompatible(t *testing.T) {
	c := requestContextEcho(nil)
	require.NoError(t, validateRequestContext(c, "itbem", requestContextTestSession("itbem", true)))
	assert.Equal(t, "organization", c.Get(ContextWorkspaceMode))
}

func TestValidateRequestContextAcceptsAuthorizedOrganization(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	token, _, err := organizationcontext.Generate("subject-1", "cafettonhouse", organizationID, time.Minute)
	require.NoError(t, err)
	c := requestContextEcho(map[string]string{
		HeaderApplicationCode:     "cafettonhouse",
		HeaderWorkspaceMode:       "organization",
		HeaderOrganizationID:      organizationID.String(),
		HeaderOrganizationContext: token,
	})
	c.Set("cognito_sub", "subject-1")
	err = validateRequestContext(c, "cafettonhouse", requestContextTestSession("cafettonhouse", false, organizationID))
	require.NoError(t, err)
	assert.Equal(t, organizationID, c.Get(ContextOrganizationID))
}

func TestValidateRequestContextRequiresOrganizationToken(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	c := requestContextEcho(map[string]string{
		HeaderApplicationCode: "eventiapp",
		HeaderWorkspaceMode:   "organization",
		HeaderOrganizationID:  organizationID.String(),
	})
	c.Set("cognito_sub", "subject-1")
	err := validateRequestContext(c, "eventiapp", requestContextTestSession("eventiapp", false, organizationID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token is required")
}

func TestValidateRequestContextAllowsCredentialBootstrapWithoutCircularDependency(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	headers := map[string]string{
		HeaderApplicationCode: "eventiapp",
		HeaderWorkspaceMode:   "organization",
		HeaderOrganizationID:  organizationID.String(),
	}
	for _, path := range []string{"/api/session", "/api/session/organization-context"} {
		t.Run(path, func(t *testing.T) {
			c := requestContextEchoPath(path, headers)
			c.Set("cognito_sub", "subject-1")
			require.NoError(t, validateRequestContext(c, "eventiapp", requestContextTestSession("eventiapp", false, organizationID)))
		})
	}
}

func TestCredentialBootstrapStillRejectsPresentedInvalidToken(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	c := requestContextEchoPath("/api/session/organization-context", map[string]string{
		HeaderApplicationCode:     "eventiapp",
		HeaderWorkspaceMode:       "organization",
		HeaderOrganizationID:      organizationID.String(),
		HeaderOrganizationContext: "invalid",
	})
	c.Set("cognito_sub", "subject-1")
	require.Error(t, validateRequestContext(c, "eventiapp", requestContextTestSession("eventiapp", false, organizationID)))
}

func TestValidateRequestContextRejectsUnauthorizedOrganization(t *testing.T) {
	c := requestContextEcho(map[string]string{
		HeaderWorkspaceMode:  "organization",
		HeaderOrganizationID: uuid.Must(uuid.NewV4()).String(),
	})
	err := validateRequestContext(c, "cafettonhouse", requestContextTestSession("cafettonhouse", false))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled")
}

func TestValidateRequestContextEnforcesPlatformBoundary(t *testing.T) {
	allowed := requestContextEcho(map[string]string{HeaderWorkspaceMode: "platform"})
	require.NoError(t, validateRequestContext(allowed, "itbem", requestContextTestSession("itbem", true)))
	assert.Equal(t, "platform", allowed.Get(ContextWorkspaceMode))

	denied := requestContextEcho(map[string]string{HeaderWorkspaceMode: "platform"})
	require.Error(t, validateRequestContext(denied, "cafettonhouse", requestContextTestSession("cafettonhouse", false)))
}

func TestValidateRequestContextVerifiesOrganizationToken(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	token, _, err := organizationcontext.Generate("subject-1", "eventiapp", organizationID, time.Minute)
	require.NoError(t, err)
	c := requestContextEcho(map[string]string{
		HeaderOrganizationID:      organizationID.String(),
		HeaderOrganizationContext: token,
	})
	c.Set("cognito_sub", "subject-1")
	require.NoError(t, validateRequestContext(c, "eventiapp", requestContextTestSession("eventiapp", false, organizationID)))

	c.Request().Header.Set(HeaderOrganizationContext, token+"tampered")
	err = validateRequestContext(c, "eventiapp", requestContextTestSession("eventiapp", false, organizationID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid or expired")
}
