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
	// ownerID holds the organization's Owner membership; ownerRole is the
	// Owner role, which member management may never grant.
	ownerID   uuid.UUID
	ownerRole uuid.UUID
	// primaryRootID and operationalRootID are platform-root accounts that
	// also hold an ordinary membership in the organization.
	primaryRootID     uuid.UUID
	operationalRootID uuid.UUID
	repo              *outsiderClientRepo
}

// newMemberPathFixture wires an organization the requester is not a member
// of, holding a standard member, an Owner and two platform-root accounts
// with below-admin memberships.
func newMemberPathFixture(t *testing.T, requester models.User) memberPathFixture {
	t.Helper()
	fixture := memberPathFixture{
		clientID:          uuid.Must(uuid.NewV4()),
		targetID:          uuid.Must(uuid.NewV4()),
		memberRole:        uuid.Must(uuid.NewV4()),
		ownerID:           uuid.Must(uuid.NewV4()),
		ownerRole:         uuid.Must(uuid.NewV4()),
		primaryRootID:     uuid.Must(uuid.NewV4()),
		operationalRootID: uuid.Must(uuid.NewV4()),
	}
	member := &models.ClientRole{ID: fixture.memberRole, Code: "MEMBER", Hierarchy: 3}
	owner := &models.ClientRole{ID: fixture.ownerRole, Code: "Owner", Hierarchy: 1}
	fixture.repo = &outsiderClientRepo{memberRoles: map[uuid.UUID]*models.ClientRole{
		fixture.targetID:          member,
		fixture.ownerID:           owner,
		fixture.primaryRootID:     member,
		fixture.operationalRootID: member,
	}}

	previousClientSvc := clientSvc
	clientSvc = clientService.NewClientService(fixture.repo, &hierarchyRoleRepo{roles: map[uuid.UUID]*models.ClientRole{
		fixture.memberRole: member,
		fixture.ownerRole:  owner,
	}}, &mockClientTypeRepo{}, nil, nil, nil)
	t.Cleanup(func() { clientSvc = previousClientSvc })

	usersService.SetDefaultUserService(usersService.NewUserService(&directoryUserRepo{users: map[uuid.UUID]*models.User{
		fixture.targetID:          {ID: fixture.targetID, Email: "member@example.com"},
		fixture.ownerID:           {ID: fixture.ownerID, Email: "owner@example.com"},
		fixture.primaryRootID:     {ID: fixture.primaryRootID, Email: "root1@example.com", RootLevel: models.RootLevelPrimary},
		fixture.operationalRootID: {ID: fixture.operationalRootID, Email: "root2@example.com", RootLevel: models.RootLevelOperational},
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

func (f memberPathFixture) inviteUser(userID, roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPost, "/clients/invite", fmt.Sprintf(`{"client_id":%q,"user_id":%q,"role_id":%q}`, f.clientID, userID, roleID))
}

func (f memberPathFixture) createMemberWithRole(roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPost, "/clients/members", fmt.Sprintf(`{"client_id":%q,"role_id":%q,"email":"new-member@example.com","first_name":"New","last_name":"Member"}`, f.clientID, roleID))
}

func (f memberPathFixture) removeUser(userID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodDelete, "/clients/members/"+userID.String()+"?client_id="+f.clientID.String(), "", "user_id", userID.String())
}

func (f memberPathFixture) setRole(userID, roleID uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	return f.context(http.MethodPut, "/clients/members/"+userID.String()+"?client_id="+f.clientID.String(), fmt.Sprintf(`{"new_role_id":%q}`, roleID), "user_id", userID.String())
}

func (f memberPathFixture) invite() (echo.Context, *httptest.ResponseRecorder) {
	return f.inviteUser(f.targetID, f.memberRole)
}

func (f memberPathFixture) createMember() (echo.Context, *httptest.ResponseRecorder) {
	return f.createMemberWithRole(f.memberRole)
}

func (f memberPathFixture) remove() (echo.Context, *httptest.ResponseRecorder) {
	return f.removeUser(f.targetID)
}

func (f memberPathFixture) updateRole() (echo.Context, *httptest.ResponseRecorder) {
	return f.setRole(f.targetID, f.memberRole)
}

type memberWrite struct {
	name    string
	request func(memberPathFixture) (echo.Context, *httptest.ResponseRecorder)
	handler func(echo.Context) error
}

var memberWrites = []memberWrite{
	{name: "add existing user", request: memberPathFixture.invite, handler: InviteUser},
	{name: "create member", request: memberPathFixture.createMember, handler: CreateClientMember},
	{name: "remove member", request: memberPathFixture.remove, handler: RemoveMember},
	{name: "change member role", request: memberPathFixture.updateRole, handler: UpdateMemberRole},
}

var platformRequesters = []struct {
	name      string
	requester models.User
}{
	{name: "legacy is_root primary", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}},
	{name: "explicit level 1 primary", requester: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary}},
	{name: "operational root", requester: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}},
	{name: "legacy is_root operational root", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational}},
}

// Both platform-root levels manage teams: they take the root path in any
// organization, without needing a membership there.
func TestPlatformRootsManageTeamsOutsideTheirOrganizations(t *testing.T) {
	for _, platform := range platformRequesters {
		for _, action := range memberWrites {
			t.Run(platform.name+" "+action.name, func(t *testing.T) {
				fixture := newMemberPathFixture(t, platform.requester)
				c, rec := action.request(fixture)

				require.NoError(t, action.handler(c))

				expected := http.StatusOK
				if action.name == "create member" {
					expected = http.StatusCreated
				}
				assert.Equal(t, expected, rec.Code, rec.Body.String())
				assert.Equal(t, 1, fixture.repo.writes)
			})
		}
	}
}

// Managing teams never includes Owner: no platform root can grant it,
// change an Owner's role or remove an Owner through member management.
func TestPlatformRootsNeverGrantOrTouchOwner(t *testing.T) {
	cases := []memberWrite{
		{name: "add existing user as Owner", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
			return f.inviteUser(f.targetID, f.ownerRole)
		}, handler: InviteUser},
		{name: "create member as Owner", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
			return f.createMemberWithRole(f.ownerRole)
		}, handler: CreateClientMember},
		{name: "promote member to Owner", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
			return f.setRole(f.targetID, f.ownerRole)
		}, handler: UpdateMemberRole},
		{name: "change the Owner's role", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
			return f.setRole(f.ownerID, f.memberRole)
		}, handler: UpdateMemberRole},
		{name: "remove the Owner", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
			return f.removeUser(f.ownerID)
		}, handler: RemoveMember},
	}
	for _, platform := range platformRequesters {
		for _, action := range cases {
			t.Run(platform.name+" "+action.name, func(t *testing.T) {
				fixture := newMemberPathFixture(t, platform.requester)
				c, rec := action.request(fixture)

				require.NoError(t, action.handler(c))

				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Zero(t, fixture.repo.writes, "no membership may change")
			})
		}
	}
}

// An operational root manages teams of standard users only: it may not add,
// re-role or remove a platform-root account, whatever that account's level.
func TestOperationalRootNeverTouchesPlatformRootAccounts(t *testing.T) {
	operational := []models.User{
		{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational},
		{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelOperational},
	}
	targets := []struct {
		name   string
		target func(memberPathFixture) uuid.UUID
	}{
		{name: "primary root", target: func(f memberPathFixture) uuid.UUID { return f.primaryRootID }},
		{name: "operational root", target: func(f memberPathFixture) uuid.UUID { return f.operationalRootID }},
	}
	for _, requester := range operational {
		for _, target := range targets {
			actions := []memberWrite{
				{name: "add existing user", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
					return f.inviteUser(target.target(f), f.memberRole)
				}, handler: InviteUser},
				{name: "remove member", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
					return f.removeUser(target.target(f))
				}, handler: RemoveMember},
				{name: "change member role", request: func(f memberPathFixture) (echo.Context, *httptest.ResponseRecorder) {
					return f.setRole(target.target(f), f.memberRole)
				}, handler: UpdateMemberRole},
			}
			for _, action := range actions {
				t.Run(fmt.Sprintf("is_root=%t %s on %s", requester.IsRoot, action.name, target.name), func(t *testing.T) {
					fixture := newMemberPathFixture(t, requester)
					c, rec := action.request(fixture)

					require.NoError(t, action.handler(c))

					assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
					assert.Zero(t, fixture.repo.writes, "no membership may change")
				})
			}
		}
	}
}

// A standard user still needs Owner/Admin in the organization: platform-level
// team management is not extended to ordinary accounts.
func TestStandardUserCannotManageMembersOutsideItsOrganizations(t *testing.T) {
	requester := models.User{ID: uuid.Must(uuid.NewV4())}
	for _, action := range memberWrites {
		t.Run(action.name, func(t *testing.T) {
			fixture := newMemberPathFixture(t, requester)
			c, rec := action.request(fixture)

			require.NoError(t, action.handler(c))

			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Zero(t, fixture.repo.writes, "no membership may change")
		})
	}
}

// A platform root entering through a branded product is scoped to an
// ordinary identity there, so it falls back to the membership path.
func TestPlatformRootOnBrandedProductCannotManageMembersOutsideItsOrganizations(t *testing.T) {
	for _, platform := range platformRequesters {
		for _, action := range memberWrites {
			t.Run(platform.name+" "+action.name, func(t *testing.T) {
				fixture := newMemberPathFixture(t, platform.requester)
				c, rec := action.request(fixture)
				c.Set("tenant_code", "cafettonhouse")

				require.NoError(t, action.handler(c))

				assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
				assert.Zero(t, fixture.repo.writes, "no membership may change")
			})
		}
	}
}
