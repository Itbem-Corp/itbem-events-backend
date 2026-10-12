package clients

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/dtos"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"
	clientService "events-stocks/services/clients"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingListClientRepo struct {
	mockClientRepo
	queries      []dtos.ClientsListQuery
	getMyClients int
}

func (m *recordingListClientRepo) ListClientsPaginated(_ *uuid.UUID, query dtos.ClientsListQuery) ([]models.Client, int64, error) {
	m.queries = append(m.queries, query)
	return nil, 0, nil
}

func (m *recordingListClientRepo) GetClientsByUser(uuid.UUID) ([]models.Client, error) {
	m.getMyClients++
	return nil, nil
}

func listMyClientsCtx(target, tenant string) (echo.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(httptest.NewRequest(http.MethodGet, target, nil), rec)
	ctx.Set("cognito_sub", "client-controller-test-sub")
	ctx.Set("tenant_code", tenant)
	ctx.Set(applicationaccess.ContextKey, &applications.Session{Application: models.Application{Code: tenant}})
	return ctx, rec
}

// ROLES-N2: an EventiApp member session (the workspace switcher's GET
// /api/clients) must list only the EventiApp reach — inheritance stops at
// the ITBEM / Cafetton House portal organizations. Both the plain and the
// paginated forms carry the session application to the repository.
func TestListMyClients_MemberSessionIsScopedToItsApplication(t *testing.T) {
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))
	previous := clientSvc
	t.Cleanup(func() { clientSvc = previous })

	for _, target := range []string{"/api/clients", "/api/clients?page=1&page_size=20"} {
		repo := &recordingListClientRepo{}
		clientSvc = clientService.NewClientService(repo, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil)
		ctx, rec := listMyClientsCtx(target, "eventiapp")
		require.NoError(t, ListMyClients(ctx))
		assert.Equal(t, http.StatusOK, rec.Code, target)
		assert.Zero(t, repo.getMyClients, "%s must not use the unscoped cached list", target)
		require.Len(t, repo.queries, 1, target)
		assert.Equal(t, "eventiapp", repo.queries[0].ApplicationCode, target)
		assert.Empty(t, repo.queries[0].TenantCode, "EventiApp is the platform tree: no tenant subtree filter")
	}

	repo := &recordingListClientRepo{}
	clientSvc = clientService.NewClientService(repo, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil)
	ctx, _ := listMyClientsCtx("/api/clients", "itbem")
	require.NoError(t, ListMyClients(ctx))
	require.Len(t, repo.queries, 1)
	assert.Equal(t, "itbem", repo.queries[0].ApplicationCode)
}

type portalReachRepo struct {
	roleClientRepo
	reachable map[uuid.UUID]bool
	appCodes  []string
}

func (m *portalReachRepo) ReachableClientIDsInApplication(_ uuid.UUID, applicationCode string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	m.appCodes = append(m.appCodes, applicationCode)
	result := map[uuid.UUID]bool{}
	for _, id := range ids {
		if m.reachable[id] {
			result[id] = true
		}
	}
	return result, nil
}

func subClientsCtx(parentID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	ctx, rec := listMyClientsCtx("/api/clients/children?parent_id="+parentID.String(), "eventiapp")
	return ctx, rec
}

// ROLES-N2 follow-up: an EventiApp owner of the platform organization lists
// its children without the ITBEM / Cafetton House portals, and cannot open a
// portal as parent.
func TestGetMySubClients_MemberDoesNotSeeOtherProductPortals(t *testing.T) {
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))
	previous := clientSvc
	t.Cleanup(func() { clientSvc = previous })

	platform := uuid.Must(uuid.NewV4())
	customer := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "QA-AUTOTEST Eventos"}
	itbem := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "ITBEM", Code: "itbem"}
	cafetton := models.Client{ID: uuid.Must(uuid.NewV4()), Name: "Cafetton House", Code: "cafettonhouse"}
	repo := &portalReachRepo{
		roleClientRepo: roleClientRepo{allowed: true, role: "OWNER", children: []models.Client{customer, itbem, cafetton}},
		reachable:      map[uuid.UUID]bool{platform: true, customer.ID: true},
	}
	clientSvc = clientService.NewClientService(repo, &mockClientRoleRepo{}, &mockClientTypeRepo{}, nil, nil, nil)

	ctx, rec := subClientsCtx(platform)
	require.NoError(t, GetMySubClients(ctx))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), customer.ID.String())
	assert.NotContains(t, rec.Body.String(), itbem.ID.String())
	assert.NotContains(t, rec.Body.String(), cafetton.ID.String())
	assert.Equal(t, []string{"eventiapp"}, repo.appCodes)

	// Inherited access to the ITBEM portal does not count in an EventiApp session.
	repo.children = nil
	ctx, rec = subClientsCtx(itbem.ID)
	require.NoError(t, GetMySubClients(ctx))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
