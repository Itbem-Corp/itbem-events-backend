package sessions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/services/applications"
	clientsService "events-stocks/services/clients"
	"events-stocks/services/ports"
	usersService "events-stocks/services/users"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var memberApplicationHierarchy = map[string]int{"OWNER": 1, "ADMIN": 2, "EDITOR": 4, "GUEST": 8}

// memberOrganization is one organization: effective role per user and the
// stored account of every user.
type memberOrganization struct {
	clientID uuid.UUID
	roles    map[uuid.UUID]string
	users    map[uuid.UUID]models.User
}

func (o *memberOrganization) add(roleCode string, user models.User) models.User {
	user.ID = uuid.Must(uuid.NewV4())
	o.users[user.ID] = user
	if roleCode != "" {
		o.roles[user.ID] = roleCode
	}
	return user
}

func (o *memberOrganization) checkAccess(userID, clientID uuid.UUID) (bool, string) {
	role, ok := o.roles[userID]
	return ok && clientID == o.clientID, role
}

type organizationClientRepo struct {
	ports.ClientRepository
	organization *memberOrganization
}

func (r organizationClientRepo) CheckAccessRecursive(userID, clientID uuid.UUID) (bool, string) {
	return r.organization.checkAccess(userID, clientID)
}

func (r organizationClientRepo) GetMemberRole(uuid.UUID, uuid.UUID) (*models.ClientRole, error) {
	return nil, gorm.ErrRecordNotFound
}

type hierarchyRoleRepo struct{ ports.ClientRoleRepository }

func (hierarchyRoleRepo) GetByCode(code string) (*models.ClientRole, error) {
	hierarchy, ok := memberApplicationHierarchy[strings.ToUpper(code)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &models.ClientRole{Code: code, Hierarchy: hierarchy}, nil
}

type organizationUserRepo struct {
	ports.UserRepository
	organization *memberOrganization
}

func (r organizationUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	user, ok := r.organization.users[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

// newMemberOrganization wires the controller to an organization and to a
// session service whose database refuses every call: a request that passes
// authorization reaches it and fails with 400, a denied one never does.
func newMemberOrganization(t *testing.T) *memberOrganization {
	t.Helper()
	organization := &memberOrganization{clientID: uuid.Must(uuid.NewV4()), roles: map[uuid.UUID]string{}, users: map[uuid.UUID]models.User{}}

	clientsService.SetDefaultClientService(clientsService.NewClientService(organizationClientRepo{organization: organization}, hierarchyRoleRepo{}, nil, nil, nil, nil))
	t.Cleanup(func() { clientsService.SetDefaultClientService(nil) })
	usersService.SetDefaultUserService(usersService.NewUserService(organizationUserRepo{organization: organization}, nil, nil))
	t.Cleanup(func() { usersService.SetDefaultUserService(nil) })

	sqlDB, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	previous := sessionService
	Configure(applications.NewSessionService(db, nil))
	t.Cleanup(func() { sessionService = previous })
	return organization
}

func (o *memberOrganization) toggle(t *testing.T, requester, target models.User) *httptest.ResponseRecorder {
	t.Helper()
	t.Cleanup(authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
		CheckAccessRecursive: o.checkAccess,
	}))
	path := "/api/clients/" + o.clientID.String() + "/member-applications/" + target.ID.String() + "/eventiapp"
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"is_active":false}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set("cognito_sub", "member-applications-test-sub")
	c.SetParamNames("id", "user_id", "application_code")
	c.SetParamValues(o.clientID.String(), target.ID.String(), "eventiapp")
	require.NoError(t, SetMemberApplication(c))
	return rec
}

func assertToggleDenied(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func assertToggleAuthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Could not update application access", "authorized requests reach the session service")
}

func TestSetMemberApplicationProtectsHigherRankedTargets(t *testing.T) {
	organization := newMemberOrganization(t)
	owner := organization.add("OWNER", models.User{})
	coOwner := organization.add("OWNER", models.User{})
	admin := organization.add("ADMIN", models.User{})
	coAdmin := organization.add("ADMIN", models.User{})
	cases := []struct {
		name      string
		requester models.User
		target    models.User
	}{
		{name: "admin on owner", requester: admin, target: owner},
		{name: "admin on another admin", requester: admin, target: coAdmin},
		{name: "admin on itself", requester: admin, target: admin},
		{name: "owner on another owner", requester: owner, target: coOwner},
		{name: "owner on itself", requester: owner, target: owner},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			assertToggleDenied(t, organization.toggle(t, test.requester, test.target))
		})
	}
}

func TestSetMemberApplicationAllowsLowerRankedTargets(t *testing.T) {
	organization := newMemberOrganization(t)
	owner := organization.add("OWNER", models.User{})
	admin := organization.add("ADMIN", models.User{})
	editor := organization.add("EDITOR", models.User{})

	assertToggleAuthorized(t, organization.toggle(t, owner, admin))
	assertToggleAuthorized(t, organization.toggle(t, owner, editor))
	assertToggleAuthorized(t, organization.toggle(t, admin, editor))
}

func TestSetMemberApplicationProtectsPlatformRootTargetsFromOrganizationManagers(t *testing.T) {
	organization := newMemberOrganization(t)
	owner := organization.add("OWNER", models.User{})
	for name, root := range map[string]models.User{
		"primary root":     organization.add("Guest", models.User{RootLevel: models.RootLevelPrimary}),
		"legacy is_root":   organization.add("Guest", models.User{IsRoot: true}),
		"operational root": organization.add("Guest", models.User{RootLevel: models.RootLevelOperational}),
	} {
		t.Run(name, func(t *testing.T) {
			assertToggleDenied(t, organization.toggle(t, owner, root))
		})
	}
}

func TestSetMemberApplicationOperationalRootStopsAtOwnerAndRoots(t *testing.T) {
	organization := newMemberOrganization(t)
	operational := organization.add("", models.User{RootLevel: models.RootLevelOperational})
	owner := organization.add("OWNER", models.User{})
	admin := organization.add("ADMIN", models.User{})
	primary := organization.add("Guest", models.User{RootLevel: models.RootLevelPrimary})

	assertToggleDenied(t, organization.toggle(t, operational, owner))
	assertToggleDenied(t, organization.toggle(t, operational, primary))
	assertToggleAuthorized(t, organization.toggle(t, operational, admin))
}

func TestSetMemberApplicationPrimaryRootActsOnAnyMember(t *testing.T) {
	organization := newMemberOrganization(t)
	primary := organization.add("", models.User{RootLevel: models.RootLevelPrimary})
	owner := organization.add("OWNER", models.User{})
	otherRoot := organization.add("Guest", models.User{RootLevel: models.RootLevelOperational})

	assertToggleAuthorized(t, organization.toggle(t, primary, owner))
	assertToggleAuthorized(t, organization.toggle(t, primary, otherRoot))
}

// An organization role in another organization never reaches this one.
func TestSetMemberApplicationDeniesManagersOfAnotherOrganization(t *testing.T) {
	organization := newMemberOrganization(t)
	editor := organization.add("EDITOR", models.User{})
	foreignOwner := models.User{ID: uuid.Must(uuid.NewV4())}
	organization.users[foreignOwner.ID] = foreignOwner

	assertToggleDenied(t, organization.toggle(t, foreignOwner, editor))
}
