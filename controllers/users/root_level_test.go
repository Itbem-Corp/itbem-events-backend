package users

import (
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/services/ports"
	userService "events-stocks/services/users"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// rootLevelUserRepo is an in-memory user store for the root-level handler.
type rootLevelUserRepo struct {
	users  map[uuid.UUID]models.User
	writes int
}

func (r *rootLevelUserRepo) CreateUser(*models.User) error { return nil }
func (r *rootLevelUserRepo) UpdateUser(*models.User) error { return nil }
func (r *rootLevelUserRepo) DeleteUser(uuid.UUID) error    { return nil }
func (r *rootLevelUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}
func (r *rootLevelUserRepo) GetUserByCognitoSub(string) (*models.User, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *rootLevelUserRepo) GetUserByEmail(string) (*models.User, error) {
	return nil, gorm.ErrRecordNotFound
}
func (r *rootLevelUserRepo) UpdateUserFields(id uuid.UUID, fields map[string]interface{}) error {
	r.writes++
	user := r.users[id]
	if level, ok := fields["root_level"].(int); ok {
		user.RootLevel = level
	}
	if isRoot, ok := fields["is_root"].(bool); ok {
		user.IsRoot = isRoot
	}
	r.users[id] = user
	return nil
}
func (r *rootLevelUserRepo) ClearProfileImage(uuid.UUID) error    { return nil }
func (r *rootLevelUserRepo) SetUserRoot(uuid.UUID, bool) error    { return nil }
func (r *rootLevelUserRepo) ListAllUsers() ([]models.User, error) { return nil, nil }
func (r *rootLevelUserRepo) SetUserActive(uuid.UUID, bool) error  { return nil }
func (r *rootLevelUserRepo) ListAllUsersPaginated(dtos.AdminUsersListQuery) ([]models.User, int64, error) {
	return nil, 0, nil
}

var _ ports.UserRepository = (*rootLevelUserRepo)(nil)

func (r *rootLevelUserRepo) levelOf(id uuid.UUID) int {
	user := r.users[id]
	return user.EffectiveRootLevel()
}

func rootLevelContext(t *testing.T, requester models.User, repo *rootLevelUserRepo, targetID, body, tenantCode string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	previousUserSvc, previousAdminSvc := userSvc, adminSvc
	userSvc = userService.NewUserService(repo, nil, nil)
	adminSvc = userService.NewAdminUserService(repo, nil, nil)
	t.Cleanup(func() { userSvc, adminSvc = previousUserSvc, previousAdminSvc })
	restore := authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
	})
	t.Cleanup(restore)

	c, rec := newEchoCtx(http.MethodPut, "/users/"+targetID+"/root-level", body)
	c.Set("cognito_sub", "root-level-test-sub")
	if tenantCode != "" {
		c.Set("tenant_code", tenantCode)
	}
	c.SetParamNames("id")
	c.SetParamValues(targetID)
	return c, rec
}

func newRootLevelRepo(users ...models.User) *rootLevelUserRepo {
	repo := &rootLevelUserRepo{users: map[uuid.UUID]models.User{}}
	for _, user := range users {
		repo.users[user.ID] = user
	}
	return repo
}

func TestUpdateUserRootLevelProtectsPrimaryRootTargets(t *testing.T) {
	requester := models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}
	targets := []struct {
		name   string
		target models.User
	}{
		{name: "another level 1 account", target: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelPrimary}},
		{name: "legacy is_root account", target: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true}},
		{name: "the requester itself", target: requester},
	}
	for _, test := range targets {
		t.Run(test.name, func(t *testing.T) {
			repo := newRootLevelRepo(requester, test.target)
			c, rec := rootLevelContext(t, requester, repo, test.target.ID.String(), `{"root_level":0}`, "")

			require.NoError(t, UpdateUserRootLevel(c))

			assert.Equal(t, http.StatusConflict, rec.Code)
			assert.Contains(t, rec.Body.String(), "Protected account")
			assert.Zero(t, repo.writes, "a protected account must never be written")
			assert.Equal(t, test.target.EffectiveRootLevel(), repo.levelOf(test.target.ID))
		})
	}
}

func TestUpdateUserRootLevelRequiresPrimaryRootOnTheRootProduct(t *testing.T) {
	target := models.User{ID: uuid.Must(uuid.NewV4())}
	requesters := []struct {
		name       string
		requester  models.User
		tenantCode string
	}{
		{name: "operational root", requester: models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}},
		{name: "standard user", requester: models.User{ID: uuid.Must(uuid.NewV4())}},
		{name: "primary root through another product", requester: models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}, tenantCode: "cafettonhouse"},
	}
	for _, test := range requesters {
		t.Run(test.name, func(t *testing.T) {
			repo := newRootLevelRepo(target)
			c, rec := rootLevelContext(t, test.requester, repo, target.ID.String(), `{"root_level":2}`, test.tenantCode)

			require.NoError(t, UpdateUserRootLevel(c))

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), "Primary platform administrator required")
			assert.Zero(t, repo.writes)
		})
	}
}

func TestUpdateUserRootLevelGrantsAndRevokesOperationalLevel(t *testing.T) {
	requester := models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}
	target := models.User{ID: uuid.Must(uuid.NewV4()), Email: "support@example.com"}
	repo := newRootLevelRepo(requester, target)

	c, rec := rootLevelContext(t, requester, repo, target.ID.String(), `{"root_level":2}`, "")
	require.NoError(t, UpdateUserRootLevel(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"root_level":2`)
	assert.Equal(t, models.RootLevelOperational, repo.levelOf(target.ID))

	c, rec = rootLevelContext(t, requester, repo, target.ID.String(), `{"root_level":0}`, "")
	require.NoError(t, UpdateUserRootLevel(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"root_level":0`)
	assert.Equal(t, models.RootLevelNone, repo.levelOf(target.ID))
}

func TestUpdateUserRootLevelRejectsInvalidLevelAndUnknownUser(t *testing.T) {
	requester := models.User{ID: uuid.Must(uuid.NewV4()), IsRoot: true, RootLevel: models.RootLevelPrimary}
	target := models.User{ID: uuid.Must(uuid.NewV4())}

	repo := newRootLevelRepo(requester, target)
	c, rec := rootLevelContext(t, requester, repo, target.ID.String(), `{"root_level":1}`, "")
	require.NoError(t, UpdateUserRootLevel(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	c, rec = rootLevelContext(t, requester, repo, uuid.Must(uuid.NewV4()).String(), `{"root_level":2}`, "")
	require.NoError(t, UpdateUserRootLevel(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Zero(t, repo.writes)
}
