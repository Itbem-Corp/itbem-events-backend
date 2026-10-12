package users

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/services/ports"
	usersService "events-stocks/services/users"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type guardUserRepo struct {
	users   map[uuid.UUID]*models.User
	deleted []uuid.UUID
}

func (r *guardUserRepo) CreateUser(*models.User) error { return nil }
func (r *guardUserRepo) UpdateUser(*models.User) error { return nil }
func (r *guardUserRepo) DeleteUser(id uuid.UUID) error {
	r.deleted = append(r.deleted, id)
	return nil
}
func (r *guardUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	if u, ok := r.users[id]; ok {
		clone := *u
		return &clone, nil
	}
	return nil, errors.New("record not found")
}
func (r *guardUserRepo) GetUserByCognitoSub(string) (*models.User, error) {
	return nil, errors.New("nf")
}
func (r *guardUserRepo) GetUserByEmail(string) (*models.User, error) { return nil, errors.New("nf") }
func (r *guardUserRepo) UpdateUserFields(uuid.UUID, map[string]interface{}) error {
	return nil
}
func (r *guardUserRepo) ClearProfileImage(uuid.UUID) error    { return nil }
func (r *guardUserRepo) SetUserRoot(uuid.UUID, bool) error    { return nil }
func (r *guardUserRepo) ListAllUsers() ([]models.User, error) { return nil, nil }
func (r *guardUserRepo) SetUserActive(uuid.UUID, bool) error  { return nil }
func (r *guardUserRepo) ListAllUsersPaginated(dtos.AdminUsersListQuery) ([]models.User, int64, error) {
	return nil, 0, nil
}

var _ ports.UserRepository = (*guardUserRepo)(nil)

type guardAuthRepo struct{ deleted []string }

func (a *guardAuthRepo) GetUser(string, string) (*dtos.AuthUser, error)     { return nil, nil }
func (a *guardAuthRepo) UpdateUser(string, map[string]string, string) error { return nil }
func (a *guardAuthRepo) DeleteUser(sub string, _ string) error {
	a.deleted = append(a.deleted, sub)
	return nil
}
func (a *guardAuthRepo) SetUserEnabled(string, bool, string) error { return nil }
func (a *guardAuthRepo) InviteUser(string, string, string, string) (*dtos.AuthUser, error) {
	return nil, nil
}
func (a *guardAuthRepo) CreateUser(dtos.CreateAuthUserRequest, string) (*dtos.AuthUser, error) {
	return nil, nil
}

var _ ports.AuthProviderRepository = (*guardAuthRepo)(nil)

type guardClientRepo struct {
	ports.ClientRepository
	memberships []models.Client
}

func (g *guardClientRepo) ListClientsByUser(uuid.UUID) ([]models.Client, error) {
	return g.memberships, nil
}

func setupGuardedAdmin(t *testing.T, actor *models.User, others []*models.User, memberships []models.Client) (*guardUserRepo, *guardAuthRepo) {
	t.Helper()
	repo := &guardUserRepo{users: map[uuid.UUID]*models.User{actor.ID: actor}}
	for _, u := range others {
		repo.users[u.ID] = u
	}
	auth := &guardAuthRepo{}
	orig := adminSvc
	adminSvc = usersService.NewAdminUserService(repo, &guardClientRepo{memberships: memberships}, auth)
	restore := authz.ReplaceHooksForTest(authz.Hooks{SyncUser: func(sub string) (*models.User, error) {
		if sub != actor.CognitoSub {
			return nil, errors.New("user not found")
		}
		return actor, nil
	}})
	t.Cleanup(func() { adminSvc = orig; restore() })
	return repo, auth
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload.Error
}

func callDeleteUserDetail(t *testing.T, actorSub string, target uuid.UUID) (int, string) {
	t.Helper()
	c, rec := newEchoCtx(http.MethodDelete, "/users/"+target.String(), "")
	c.Set("cognito_sub", actorSub)
	c.SetParamNames("id")
	c.SetParamValues(target.String())
	require.NoError(t, DeleteUserDetail(c))
	return rec.Code, errorCode(t, rec.Body.Bytes())
}

func primaryRootActor() *models.User {
	return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "actor-root-1", RootLevel: models.RootLevelPrimary, IsActive: true}
}

// ADM-9 regressions: backend guards for user delete/deactivate.
// A primary root acting on itself gets the #285 primary-root contract.
func TestDeleteUserDetail_Self_Returns409ProtectedAccount(t *testing.T) {
	actor := primaryRootActor()
	repo, auth := setupGuardedAdmin(t, actor, nil, nil)

	status, code := callDeleteUserDetail(t, actor.CognitoSub, actor.ID)

	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, ErrCodeUserPrimaryRootProtected, code)
	assert.Empty(t, repo.deleted)
	assert.Empty(t, auth.deleted)
}

func TestDeleteUserDetail_OtherPrimaryRoot_Returns409ProtectedAccount(t *testing.T) {
	actor := primaryRootActor()
	other := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "other-root", IsRoot: true}
	repo, auth := setupGuardedAdmin(t, actor, []*models.User{other}, nil)

	status, code := callDeleteUserDetail(t, actor.CognitoSub, other.ID)

	// One contract with #285: 409 "Protected account" + stable code.
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, ErrCodeUserPrimaryRootProtected, code)
	assert.Empty(t, repo.deleted)
	assert.Empty(t, auth.deleted)
}

func TestDeleteUserDetail_WithMemberships_Returns409(t *testing.T) {
	actor := primaryRootActor()
	target := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "member"}
	repo, auth := setupGuardedAdmin(t, actor, []*models.User{target}, []models.Client{{ID: uuid.Must(uuid.NewV4())}})

	status, code := callDeleteUserDetail(t, actor.CognitoSub, target.ID)

	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, ErrCodeUserHasActiveMemberships, code)
	assert.Empty(t, repo.deleted)
	assert.Empty(t, auth.deleted)
}

func TestDeleteUserDetail_PlainUserWithoutMemberships_Deletes(t *testing.T) {
	actor := primaryRootActor()
	target := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "plain"}
	repo, auth := setupGuardedAdmin(t, actor, []*models.User{target}, nil)

	status, _ := callDeleteUserDetail(t, actor.CognitoSub, target.ID)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, []uuid.UUID{target.ID}, repo.deleted)
	assert.Equal(t, []string{"plain"}, auth.deleted)
}

// A primary root acting on itself gets the #285 primary-root contract.
func TestDeactivateUser_Self_Returns409ProtectedAccount(t *testing.T) {
	actor := primaryRootActor()
	setupGuardedAdmin(t, actor, nil, nil)

	c, rec := newEchoCtx(http.MethodPut, "/users/"+actor.ID.String()+"/deactivate", "")
	c.Set("cognito_sub", actor.CognitoSub)
	c.SetParamNames("id")
	c.SetParamValues(actor.ID.String())
	require.NoError(t, DeactivateUser(c))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, ErrCodeUserPrimaryRootProtected, errorCode(t, rec.Body.Bytes()))
}

func TestDeactivateUser_OtherPrimaryRoot_Returns409ProtectedAccount(t *testing.T) {
	actor := primaryRootActor()
	other := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "other-root", RootLevel: models.RootLevelPrimary}
	setupGuardedAdmin(t, actor, []*models.User{other}, nil)

	c, rec := newEchoCtx(http.MethodPut, "/users/"+other.ID.String()+"/deactivate", "")
	c.Set("cognito_sub", actor.CognitoSub)
	c.SetParamNames("id")
	c.SetParamValues(other.ID.String())
	require.NoError(t, DeactivateUser(c))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, ErrCodeUserPrimaryRootProtected, errorCode(t, rec.Body.Bytes()))
	assert.Contains(t, rec.Body.String(), "Protected account")
}
