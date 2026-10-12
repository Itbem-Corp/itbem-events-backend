package clientroles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	clientrolesService "events-stocks/services/clientroles"
	clientsService "events-stocks/services/clients"
	"events-stocks/services/ports"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outsiderClientRepo is an organization the requester holds no membership in.
type outsiderClientRepo struct{ ports.ClientRepository }

func (outsiderClientRepo) CheckAccessRecursive(uuid.UUID, uuid.UUID) (bool, string) {
	return false, ""
}

// seededRoleRepo mirrors seeds/ClientRoleSeed.go.
type seededRoleRepo struct{ ports.ClientRoleRepository }

var seededRoles = []models.ClientRole{
	{Code: "Owner", Hierarchy: 1},
	{Code: "Admin", Hierarchy: 2},
	{Code: "EVENT_MANAGER", Hierarchy: 3},
	{Code: "EDITOR", Hierarchy: 4},
	{Code: "CHECKIN", Hierarchy: 5},
	{Code: "ANALYST", Hierarchy: 6},
	{Code: "Member", Hierarchy: 7},
	{Code: "Guest", Hierarchy: 8},
}

func (seededRoleRepo) GetByCode(code string) (*models.ClientRole, error) {
	for _, role := range seededRoles {
		if strings.EqualFold(role.Code, code) {
			copy := role
			return &copy, nil
		}
	}
	return nil, assert.AnError
}

func (seededRoleRepo) GetAssignableRoles(hierarchy int) ([]models.ClientRole, error) {
	roles := []models.ClientRole{}
	for _, role := range seededRoles {
		if role.Hierarchy > hierarchy {
			roles = append(roles, role)
		}
	}
	return roles, nil
}

func listRolesAs(t *testing.T, requester models.User) *httptest.ResponseRecorder {
	t.Helper()
	previousClients, previousRoles := clientSvc, clientRoleSvc
	t.Cleanup(func() { clientSvc, clientRoleSvc = previousClients, previousRoles })
	InitClientRolesController(
		clientsService.NewClientService(outsiderClientRepo{}, seededRoleRepo{}, nil, nil, nil, nil),
		clientrolesService.NewClientRoleService(seededRoleRepo{}),
	)
	t.Cleanup(authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/catalogs/roles?client_id="+uuid.Must(uuid.NewV4()).String(), nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("cognito_sub", "client-roles-test-sub")
	require.NoError(t, ListClientRoles(c))
	return rec
}

func roleCodes(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct {
		Data []struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	codes := make([]string, 0, len(body.Data))
	for _, role := range body.Data {
		codes = append(codes, role.Code)
	}
	return codes
}

// A primary root manages teams in any organization and holds every
// capability, so it gets every role below Owner without a membership there.
func TestPrimaryRootsListEveryRoleBelowOwnerInAnyOrganization(t *testing.T) {
	for name, requester := range map[string]models.User{
		"legacy is_root primary":   {ID: uuid.Must(uuid.NewV4()), IsRoot: true},
		"explicit level 1 primary": {ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary},
	} {
		t.Run(name, func(t *testing.T) {
			rec := listRolesAs(t, requester)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t,
				[]string{"Admin", "EVENT_MANAGER", "EDITOR", "CHECKIN", "ANALYST", "Member", "Guest"},
				roleCodes(t, rec), "Owner is never assignable from member management")
		})
	}
}

// A Root 2 also manages teams in any organization, but the catalog offers
// only the roles inside its own capabilities: never Admin, Event manager or
// Editor, which carry event or organization powers it lacks.
func TestOperationalRootListsOnlyRolesWithinItsCapabilities(t *testing.T) {
	for name, requester := range map[string]models.User{
		"operational root":                {ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational},
		"legacy is_root operational root": {ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational},
	} {
		t.Run(name, func(t *testing.T) {
			rec := listRolesAs(t, requester)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, []string{"CHECKIN", "ANALYST", "Member", "Guest"}, roleCodes(t, rec))
		})
	}
}

func TestStandardUserCannotListRolesOutsideItsOrganizations(t *testing.T) {
	rec := listRolesAs(t, models.User{ID: uuid.Must(uuid.NewV4())})

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// A platform root entering a branded product is an ordinary identity there.
func TestPlatformRootOnBrandedProductCannotListRolesOutsideItsOrganizations(t *testing.T) {
	previousClients, previousRoles := clientSvc, clientRoleSvc
	t.Cleanup(func() { clientSvc, clientRoleSvc = previousClients, previousRoles })
	InitClientRolesController(
		clientsService.NewClientService(outsiderClientRepo{}, seededRoleRepo{}, nil, nil, nil, nil),
		clientrolesService.NewClientRoleService(seededRoleRepo{}),
	)
	t.Cleanup(authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}, nil
		},
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/catalogs/roles?client_id="+uuid.Must(uuid.NewV4()).String(), nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("cognito_sub", "client-roles-test-sub")
	c.Set("tenant_code", "cafettonhouse")

	require.NoError(t, ListClientRoles(c))

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}
