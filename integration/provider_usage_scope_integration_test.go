//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	automation "events-stocks/controllers/automation"
	"events-stocks/internal/authz"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

const providerUsageScopeDummyMarker = "integration-test-only-marker-not-a-secret"

func TestProjectProviderUsageEnforcesOrganizationAndProjectPermissions(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	createdBy := "provider-usage-scope-" + suffix
	now := time.Now().UTC()
	clientType := models.ClientType{
		ID: uuid.Must(uuid.NewV4()), Name: "Usage scope client type " + suffix,
		Code: "USAGE_SCOPE_" + suffix, Level: 10, IsActive: true,
	}
	require.NoError(t, db.Create(&clientType).Error)

	newClient := func(name, code string, parentID *uuid.UUID) models.Client {
		client := models.Client{
			ID: uuid.Must(uuid.NewV4()), Name: name + " " + suffix,
			Code: code + "-" + suffix, ClientTypeID: clientType.ID,
			ParentID: parentID, IsActive: true,
		}
		require.NoError(t, db.Create(&client).Error)
		return client
	}
	// The selected organization is a branch beneath a shared parent. Its own
	// root project and a project several levels below it are in scope; the
	// sibling branch and a separate organization are outside the selection.
	sharedParent := newClient("Usage scope parent", "usage-parent", nil)
	organization := newClient("Usage selected organization", "usage-org", &sharedParent.ID)
	child := newClient("Usage child", "usage-child", &organization.ID)
	grandchild := newClient("Usage grandchild", "usage-grandchild", &child.ID)
	sibling := newClient("Usage sibling organization", "usage-sibling", &sharedParent.ID)
	outsideOrganization := newClient("Usage outside organization", "usage-outside", nil)

	newProject := func(client models.Client, key string) models.DeliveryProject {
		project := models.DeliveryProject{
			ID: uuid.Must(uuid.NewV4()), ClientID: client.ID,
			Name: "Usage scope " + key + " project " + suffix,
			Slug: "usage-scope-" + key + "-" + suffix, Status: "active", CreatedBy: createdBy,
			CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, db.Create(&project).Error)
		return project
	}
	rootProject := newProject(organization, "root")
	descendantProject := newProject(grandchild, "descendant")
	siblingProject := newProject(sibling, "sibling")
	outsideProject := newProject(outsideOrganization, "outside")

	rootViewer := "usage-root-viewer-" + suffix
	rootManager := "usage-root-manager-" + suffix
	descendantViewer := "usage-descendant-viewer-" + suffix
	permissionManager := "usage-permission-manager-" + suffix
	unassigned := "usage-unassigned-" + suffix
	for _, membership := range []models.DeliveryProjectMember{
		{ID: uuid.Must(uuid.NewV4()), ProjectID: rootProject.ID, CognitoSub: rootViewer, Role: "viewer", Permissions: `[]`, CreatedBy: createdBy},
		{ID: uuid.Must(uuid.NewV4()), ProjectID: rootProject.ID, CognitoSub: rootManager, Role: "delivery_manager", Permissions: `[]`, CreatedBy: createdBy},
		{ID: uuid.Must(uuid.NewV4()), ProjectID: descendantProject.ID, CognitoSub: descendantViewer, Role: "viewer", Permissions: `[]`, CreatedBy: createdBy},
		{ID: uuid.Must(uuid.NewV4()), ProjectID: descendantProject.ID, CognitoSub: permissionManager, Role: "custom", Permissions: ` ["ai:credentials:manage"] `, CreatedBy: createdBy},
		{ID: uuid.Must(uuid.NewV4()), ProjectID: siblingProject.ID, CognitoSub: rootManager, Role: "delivery_manager", Permissions: `[]`, CreatedBy: createdBy},
		{ID: uuid.Must(uuid.NewV4()), ProjectID: outsideProject.ID, CognitoSub: rootManager, Role: "delivery_manager", Permissions: `[]`, CreatedBy: createdBy},
	} {
		require.NoError(t, db.Create(&membership).Error)
	}

	users := map[string]*models.User{}
	for _, subject := range []string{rootViewer, rootManager, descendantViewer, permissionManager, unassigned} {
		users[subject] = &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: subject, IsActive: true}
	}
	restoreAuth := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(subject string) (*models.User, error) {
		user, ok := users[subject]
		if !ok {
			return nil, fmt.Errorf("unexpected integration subject %q", subject)
		}
		return user, nil
	}})
	t.Cleanup(restoreAuth)

	resolver := &providerUsageScopeResolverSpy{}
	automation.ConfigureInferenceCredentials(resolver)
	t.Cleanup(func() { automation.ConfigureInferenceCredentials(nil) })

	call := func(method string, projectID uuid.UUID, subject string) (int, string) {
		path := "/api/automation/ai/projects/" + projectID.String() + "/provider-usage"
		var handler echo.HandlerFunc = automation.GetProjectProviderUsage
		if method == http.MethodPost {
			path += "/refresh"
			handler = automation.RefreshProjectProviderUsage
		}
		request := httptest.NewRequest(method, path, nil)
		recorder := httptest.NewRecorder()
		ctx := echo.New().NewContext(request, recorder)
		if method == http.MethodPost {
			ctx.SetPath("/api/automation/ai/projects/:projectId/provider-usage/refresh")
		} else {
			ctx.SetPath("/api/automation/ai/projects/:projectId/provider-usage")
		}
		ctx.SetParamNames("projectId")
		ctx.SetParamValues(projectID.String())
		ctx.Set("cognito_sub", subject)
		ctx.Set("workspace_mode", "organization")
		ctx.Set("organization_id", organization.ID)
		require.NoError(t, handler(ctx))
		return recorder.Code, recorder.Body.String()
	}

	// A project member can read usage from a project at the selected
	// organization root or anywhere in its recursive descendant tree.
	status, body := call(http.MethodGet, rootProject.ID, rootViewer)
	require.Equal(t, http.StatusOK, status, body)
	status, body = call(http.MethodGet, descendantProject.ID, descendantViewer)
	require.Equal(t, http.StatusOK, status, body)

	// Project assignment grants read access. Refresh requires the manager role
	// or an explicit AI credential management permission.
	status, body = call(http.MethodPost, rootProject.ID, rootViewer)
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = call(http.MethodPost, descendantProject.ID, descendantViewer)
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = call(http.MethodGet, rootProject.ID, unassigned)
	require.Equal(t, http.StatusForbidden, status, body)
	status, body = call(http.MethodPost, rootProject.ID, unassigned)
	require.Equal(t, http.StatusForbidden, status, body)
	require.Empty(t, resolver.hasReads, "membership and manage denials must precede project credential lookup")
	require.Empty(t, resolver.keyReads, "membership and manage denials must precede project credential lookup")
	require.Zero(t, resolver.globalReads, "denied requests must not use a global credential")

	// A member who manages the root project and a descendant member with the
	// explicit permission can refresh. The fake resolver reports no configured
	// keys, so successful requests append only unavailable observations and
	// never open a connection to a provider.
	status, body = call(http.MethodPost, rootProject.ID, rootManager)
	require.Equal(t, http.StatusOK, status, body)
	status, body = call(http.MethodPost, descendantProject.ID, permissionManager)
	require.Equal(t, http.StatusOK, status, body)
	require.Equal(t, []string{
		rootProject.ID.String() + ":deepseek", rootProject.ID.String() + ":minimax",
		descendantProject.ID.String() + ":deepseek", descendantProject.ID.String() + ":minimax",
	}, resolver.hasReads, "credential resolution must remain project-scoped for authorized refreshes")
	require.Empty(t, resolver.keyReads, "no dummy credential is sent to a provider")
	require.Zero(t, resolver.globalReads)
	for _, project := range []models.DeliveryProject{rootProject, descendantProject} {
		var snapshotCount int64
		require.NoError(t, db.Model(&models.AutomationProviderUsageSnapshot{}).
			Where("project_id = ?", project.ID).Count(&snapshotCount).Error)
		require.Equal(t, int64(3), snapshotCount, "each authorized refresh should append its project-scoped provider observations")
	}

	// Even a manager membership on a sibling branch cannot override the selected
	// organization. The response also matches a nonexistent project exactly.
	status, siblingBody := call(http.MethodPost, siblingProject.ID, rootManager)
	require.Equal(t, http.StatusNotFound, status, siblingBody)
	status, outsideBody := call(http.MethodGet, outsideProject.ID, rootManager)
	require.Equal(t, http.StatusNotFound, status, outsideBody)
	missingProjectID := uuid.Must(uuid.NewV4())
	status, missingBody := call(http.MethodPost, missingProjectID, rootManager)
	require.Equal(t, http.StatusNotFound, status, missingBody)
	require.Equal(t, missingBody, siblingBody, "sibling and nonexistent project responses must be indistinguishable")
	require.Equal(t, missingBody, outsideBody, "out-of-scope and nonexistent project responses must be indistinguishable")
	for _, privateValue := range []string{
		siblingProject.ID.String(), sibling.ID.String(), outsideProject.ID.String(), outsideOrganization.ID.String(),
		missingProjectID.String(),
	} {
		require.NotContains(t, siblingBody, privateValue)
		require.NotContains(t, outsideBody, privateValue)
	}
	require.Len(t, resolver.hasReads, 4, "out-of-scope requests must not perform additional project credential lookups")
	require.Empty(t, resolver.keyReads)
	require.Zero(t, resolver.globalReads)
}

type providerUsageScopeResolverSpy struct {
	hasReads    []string
	keyReads    []string
	globalReads int
}

func (r *providerUsageScopeResolverSpy) APIKey(context.Context, string) (string, error) {
	r.globalReads++
	return providerUsageScopeDummyMarker, nil
}

func (*providerUsageScopeResolverSpy) ReplaceAPIKey(context.Context, string, string) error {
	return nil
}

func (r *providerUsageScopeResolverSpy) HasAPIKeyForProject(_ context.Context, projectID, provider string) (bool, error) {
	r.hasReads = append(r.hasReads, projectID+":"+provider)
	return false, nil
}

func (r *providerUsageScopeResolverSpy) APIKeyForProject(_ context.Context, projectID, provider string) (string, error) {
	r.keyReads = append(r.keyReads, projectID+":"+provider)
	return providerUsageScopeDummyMarker, nil
}

func (*providerUsageScopeResolverSpy) ReplaceProjectAPIKey(context.Context, string, string, string) error {
	return nil
}

func (*providerUsageScopeResolverSpy) DeleteProjectAPIKey(context.Context, string, string) error {
	return nil
}
