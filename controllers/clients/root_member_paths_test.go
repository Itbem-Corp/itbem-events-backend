package clients

import (
	"errors"
	"events-stocks/internal/authz"
	"events-stocks/models"
	clientService "events-stocks/services/clients"
	usersService "events-stocks/services/users"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outsiderClientRepo models an organization the requester does not belong
// to, and records every membership write.
type outsiderClientRepo struct {
	mockClientRepo
	memberRoles map[uuid.UUID]*models.ClientRole
	writes      int
}

func (r *outsiderClientRepo) CheckAccessRecursive(uuid.UUID, uuid.UUID) (bool, string) {
	return false, ""
}
func (r *outsiderClientRepo) IsMember(uuid.UUID, uuid.UUID) (bool, string) { return false, "" }
func (r *outsiderClientRepo) GetMemberRole(_ uuid.UUID, userID uuid.UUID) (*models.ClientRole, error) {
	role, ok := r.memberRoles[userID]
	if !ok {
		return nil, errors.New("record not found")
	}
	return role, nil
}
func (r *outsiderClientRepo) AddMember(*models.ClientMember) error { r.writes++; return nil }
func (r *outsiderClientRepo) RemoveMember(uuid.UUID, uuid.UUID) error {
	r.writes++
	return nil
}
func (r *outsiderClientRepo) UpdateMemberRole(uuid.UUID, uuid.UUID, uuid.UUID) error {
	r.writes++
	return nil
}

type hierarchyRoleRepo struct {
	mockClientRoleRepo
	roles map[uuid.UUID]*models.ClientRole
}

func (r *hierarchyRoleRepo) GetByID(id uuid.UUID) (*models.ClientRole, error) {
	role, ok := r.roles[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return role, nil
}

type directoryUserRepo struct {
	mockControllerUserRepo
	users map[uuid.UUID]*models.User
}

func (r *directoryUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return user, nil
}

type memberPathFixture struct {
	clientID   uuid.UUID
	targetID   uuid.UUID
	memberRole uuid.UUID
	repo       *outsiderClientRepo
}

// newMemberPathFixture wires an organization the requester is not a member
// of, holding one standard member with a below-admin role.
func newMemberPathFixture(t *testing.T, requester models.User) memberPathFixture {
	t.Helper()
	fixture := memberPathFixture{
		clientID:   uuid.Must(uuid.NewV4()),
		targetID:   uuid.Must(uuid.NewV4()),
		memberRole: uuid.Must(uuid.NewV4()),
	}
	role := &models.ClientRole{ID: fixture.memberRole, Code: "MEMBER", Hierarchy: 3}
	fixture.repo = &outsiderClientRepo{memberRoles: map[uuid.UUID]*models.ClientRole{fixture.targetID: role}}

	previousClientSvc := clientSvc
	clientSvc = clientService.NewClientService(fixture.repo, &hierarchyRoleRepo{roles: map[uuid.UUID]*models.ClientRole{fixture.memberRole: role}}, &mockClientTypeRepo{}, nil, nil, nil)
	t.Cleanup(func() { clientSvc = previousClientSvc })

	usersService.SetDefaultUserService(usersService.NewUserService(&directoryUserRepo{users: map[uuid.UUID]*models.User{
		fixture.targetID: {ID: fixture.targetID, Email: "member@example.com"},
	}}, &mockControllerAuthRepo{}, nil))
	t.Cleanup(func() { usersService.SetDefaultUserService(nil) })

	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
	})
	t.Cleanup(restore)
	return fixture
}

func (f memberPathFixture) context(method, path, body string, params ...string) (echo.Context, *httptest.ResponseRecorder) {
	c, rec := newEchoCtx(method, path, body)
	c.Set("cognito_sub", "root-member-path-test-sub")
	if len(params) == 2 {
		c.SetParamNames(params[0])
		c.SetParamValues(params[1])
	}
	return c, rec
}

func (f memberPathFixture) invite() (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPost, "/clients/invite", fmt.Sprintf(`{"client_id":%q,"user_id":%q,"role_id":%q}`, f.clientID, f.targetID, f.memberRole))
}

func (f memberPathFixture) createMember() (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPost, "/clients/members", fmt.Sprintf(`{"client_id":%q,"role_id":%q,"email":"new-member@example.com","first_name":"New","last_name":"Member"}`, f.clientID, f.memberRole))
}

func (f memberPathFixture) remove() (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodDelete, "/clients/members/"+f.targetID.String()+"?client_id="+f.clientID.String(), "", "user_id", f.targetID.String())
}

func (f memberPathFixture) updateRole() (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPut, "/clients/members/"+f.targetID.String()+"?client_id="+f.clientID.String(), fmt.Sprintf(`{"new_role_id":%q}`, f.memberRole), "user_id", f.targetID.String())
}

// A historical is_root flag must not outrank an explicit operational level:
// such an account is Root 2 everywhere else, so organization member
// management has to go through its own memberships like any other Root 2.
func TestLegacyRootFlagWithOperationalLevelCannotManageMembersOutsideItsOrganizations(t *testing.T) {
	requester := models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational}
	actions := []struct {
		name    string
		request func(memberPathFixture) (echo.Context, *httptest.ResponseRecorder)
		handler func(echo.Context) error
	}{
		{name: "add existing user", request: memberPathFixture.invite, handler: InviteUser},
		{name: "create member", request: memberPathFixture.createMember, handler: CreateClientMember},
		{name: "remove member", request: memberPathFixture.remove, handler: RemoveMember},
		{name: "change member role", request: memberPathFixture.updateRole, handler: UpdateMemberRole},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			fixture := newMemberPathFixture(t, requester)
			c, rec := action.request(fixture)

			require.NoError(t, action.handler(c))

			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Zero(t, fixture.repo.writes, "no membership may change")
		})
	}
}

func TestPrimaryRootsKeepRootMemberManagement(t *testing.T) {
	requesters := []struct {
		name      string
		requester models.User
	}{
		{name: "legacy is_root account", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}},
		{name: "explicit level 1 account", requester: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary}},
	}
	for _, test := range requesters {
		t.Run(test.name+" adds existing user", func(t *testing.T) {
			fixture := newMemberPathFixture(t, test.requester)
			c, rec := fixture.invite()
			require.NoError(t, InviteUser(c))
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fixture.repo.writes)
		})
		t.Run(test.name+" removes member", func(t *testing.T) {
			fixture := newMemberPathFixture(t, test.requester)
			c, rec := fixture.remove()
			require.NoError(t, RemoveMember(c))
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fixture.repo.writes)
		})
		t.Run(test.name+" changes member role", func(t *testing.T) {
			fixture := newMemberPathFixture(t, test.requester)
			c, rec := fixture.updateRole()
			require.NoError(t, UpdateMemberRole(c))
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fixture.repo.writes)
		})
	}
}
