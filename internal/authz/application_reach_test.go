package authz

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An EventiApp platform-org Owner inherits OWNER into every descendant, but on
// the EventiApp host the ITBEM and Cafetton House portal orgs are outside the
// session application: organization checks by id must deny them (ROLES-N2).
func TestRequireClientCapabilityStopsAtOtherApplicationPortals(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	eventiChild := uuid.Must(uuid.NewV4())
	itbemPortal := uuid.Must(uuid.NewV4())
	cafettonPortal := uuid.Must(uuid.NewV4())
	reachInEventiApp := map[uuid.UUID]bool{eventiChild: true}
	var askedApp string
	restore := ReplaceHooksForTest(Hooks{
		SyncUser: func(string) (*models.User, error) { return &models.User{ID: userID}, nil },
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) {
			return true, "INHERITED_OWNER"
		},
		ReachableClientIDsInApplication: func(gotUser uuid.UUID, app string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
			assert.Equal(t, userID, gotUser)
			askedApp = app
			out := map[uuid.UUID]bool{}
			for _, id := range ids {
				if reachInEventiApp[id] {
					out[id] = true
				}
			}
			return out, nil
		},
	})
	t.Cleanup(restore)

	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/clients", nil), httptest.NewRecorder())
	c.Set("cognito_sub", "owner")
	c.Set("tenant_code", "eventiapp")
	user, err := CurrentUser(c)
	require.NoError(t, err)
	assert.Equal(t, "eventiapp", user.SessionApplicationCode)

	require.NoError(t, RequireClientCapability(user, eventiChild, CapabilityMembersManage), "in-product inheritance still works")
	assert.Equal(t, "eventiapp", askedApp)
	require.NoError(t, RequireClientAccess(user, eventiChild))

	for _, portal := range []uuid.UUID{itbemPortal, cafettonPortal} {
		err := RequireClientCapability(user, portal, CapabilityView)
		require.Error(t, err)
		assert.Equal(t, http.StatusForbidden, err.(*Failure).Status)
		err = RequireClientAccess(user, portal)
		require.Error(t, err)
		assert.Equal(t, http.StatusForbidden, err.(*Failure).Status)
	}
}

func TestRequireClientCapabilityApplicationReachFailsClosed(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	restore := ReplaceHooksForTest(Hooks{
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "OWNER" },
	})
	t.Cleanup(restore)
	previous := hooks.ReachableClientIDsInApplication
	hooks.ReachableClientIDsInApplication = nil
	t.Cleanup(func() { hooks.ReachableClientIDsInApplication = previous })

	user := &models.User{ID: uuid.Must(uuid.NewV4()), SessionApplicationCode: "eventiapp"}
	err := RequireClientCapability(user, clientID, CapabilityView)
	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, err.(*Failure).Status)
}

func TestPlatformAdminSkipsApplicationReach(t *testing.T) {
	restore := ReplaceHooksForTest(Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}, nil
		},
		ReachableClientIDsInApplication: func(uuid.UUID, string, []uuid.UUID) (map[uuid.UUID]bool, error) {
			t.Fatal("platform admins must not be limited by the member reach")
			return nil, nil
		},
	})
	t.Cleanup(restore)
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/clients", nil), httptest.NewRecorder())
	c.Set("cognito_sub", "root")
	c.Set("tenant_code", "eventiapp")
	user, err := CurrentUser(c)
	require.NoError(t, err)
	require.True(t, user.IsPlatformAdmin())
	require.NoError(t, RequireClientCapability(user, uuid.Must(uuid.NewV4()), CapabilityOrgManage))
	require.NoError(t, RequireClientAccess(user, uuid.Must(uuid.NewV4())))
}

func TestCurrentUserDoesNotMutateTheSyncedUser(t *testing.T) {
	synced := &models.User{ID: uuid.Must(uuid.NewV4())}
	restore := ReplaceHooksForTest(Hooks{SyncUser: func(string) (*models.User, error) { return synced, nil }})
	t.Cleanup(restore)
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	c.Set("cognito_sub", "member")
	c.Set("tenant_code", "itbem")
	user, err := CurrentUser(c)
	require.NoError(t, err)
	assert.Equal(t, "itbem", user.SessionApplicationCode)
	assert.Empty(t, synced.SessionApplicationCode)
}
