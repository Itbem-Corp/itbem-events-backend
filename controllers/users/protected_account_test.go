package users

import (
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

// accountUserRepo extends the in-memory root-level store with cognito lookups
// and counts every local write that removes or disables an account.
type accountUserRepo struct {
	*rootLevelUserRepo
	deletes int
	updates int
}

func (r *accountUserRepo) GetUserByCognitoSub(sub string) (*models.User, error) {
	for _, user := range r.users {
		if user.CognitoSub == sub {
			copied := user
			return &copied, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *accountUserRepo) DeleteUser(uuid.UUID) error  { r.deletes++; return nil }
func (r *accountUserRepo) UpdateUser(*models.User) error { r.updates++; return nil }

// accountAuthRepo counts identity-provider writes.
type accountAuthRepo struct {
	ports.AuthProviderRepository
	deletes  int
	disables int
}

func (r *accountAuthRepo) DeleteUser(string, string) error { r.deletes++; return nil }
func (r *accountAuthRepo) SetUserEnabled(_ string, enabled bool, _ string) error {
	if !enabled {
		r.disables++
	}
	return nil
}

func (r *accountUserRepo) untouched(t *testing.T, auth *accountAuthRepo) {
	t.Helper()
	assert.Zero(t, r.deletes+r.updates, "no local write may happen")
	assert.Zero(t, auth.deletes+auth.disables, "the identity provider must not be touched")
}

type accountFixture struct {
	repo *accountUserRepo
	auth *accountAuthRepo
}

func newAccountFixture(t *testing.T, requester models.User, users ...models.User) accountFixture {
	t.Helper()
	fixture := accountFixture{
		repo: &accountUserRepo{rootLevelUserRepo: newRootLevelRepo(append([]models.User{requester}, users...)...)},
		auth: &accountAuthRepo{},
	}
	previousUserSvc, previousAdminSvc := userSvc, adminSvc
	userSvc = userService.NewUserService(fixture.repo, fixture.auth, nil)
	adminSvc = userService.NewAdminUserService(fixture.repo, &guardClientRepo{}, fixture.auth)
	t.Cleanup(func() { userSvc, adminSvc = previousUserSvc, previousAdminSvc })
	t.Cleanup(authz.ReplaceHooksForTest(authz.Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := requester
			return &user, nil
		},
	}))
	return fixture
}

func accountContext(method, path, cognitoSub, targetID, tenantCode string) (echo.Context, *httptest.ResponseRecorder) {
	c, rec := newEchoCtx(method, path, "")
	c.Set("cognito_sub", cognitoSub)
	if tenantCode != "" {
		c.Set("tenant_code", tenantCode)
	}
	if targetID != "" {
		c.SetParamNames("id")
		c.SetParamValues(targetID)
	}
	return c, rec
}

func primaryRootRequester() models.User {
	return models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "primary-root-sub", IsRoot: true, RootLevel: models.RootLevelPrimary}
}

func protectedTargets(requester models.User) []struct {
	name   string
	target models.User
} {
	return []struct {
		name   string
		target models.User
	}{
		{name: "another level 1 account", target: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "other-level-one", RootLevel: models.RootLevelPrimary}},
		{name: "legacy is_root account", target: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "legacy-root", IsRoot: true}},
		{name: "the requester itself", target: requester},
	}
}

func TestDeleteUserDetailProtectsPrimaryRootAccounts(t *testing.T) {
	requester := primaryRootRequester()
	for _, test := range protectedTargets(requester) {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAccountFixture(t, requester, test.target)
			c, rec := accountContext(http.MethodDelete, "/users/"+test.target.ID.String(), requester.CognitoSub, test.target.ID.String(), "")

			require.NoError(t, DeleteUserDetail(c))

			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "Protected account")
			fixture.repo.untouched(t, fixture.auth)
		})
	}
}

func TestDeactivateUserProtectsPrimaryRootAccounts(t *testing.T) {
	requester := primaryRootRequester()
	for _, test := range protectedTargets(requester) {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAccountFixture(t, requester, test.target)
			c, rec := accountContext(http.MethodPut, "/users/"+test.target.ID.String()+"/deactivate", requester.CognitoSub, test.target.ID.String(), "")

			require.NoError(t, DeactivateUser(c))

			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "Protected account")
			fixture.repo.untouched(t, fixture.auth)
		})
	}
}

func TestPrimaryRootStillDeletesAndDeactivatesOtherAccounts(t *testing.T) {
	requester := primaryRootRequester()
	for name, target := range map[string]models.User{
		"operational root": {ID: uuid.Must(uuid.NewV4()), CognitoSub: "level-two", RootLevel: models.RootLevelOperational},
		"standard user":    {ID: uuid.Must(uuid.NewV4()), CognitoSub: "standard"},
	} {
		t.Run(name+" deactivate", func(t *testing.T) {
			fixture := newAccountFixture(t, requester, target)
			c, rec := accountContext(http.MethodPut, "/users/"+target.ID.String()+"/deactivate", requester.CognitoSub, target.ID.String(), "")

			require.NoError(t, DeactivateUser(c))

			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fixture.auth.disables)
		})
		t.Run(name+" delete", func(t *testing.T) {
			fixture := newAccountFixture(t, requester, target)
			c, rec := accountContext(http.MethodDelete, "/users/"+target.ID.String(), requester.CognitoSub, target.ID.String(), "")

			require.NoError(t, DeleteUserDetail(c))

			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fixture.auth.deletes)
			assert.Equal(t, 1, fixture.repo.deletes)
		})
	}
}

// DELETE /api/users closes the caller's own account. The stored account is
// checked, not the product-scoped identity, so a primary root signed into a
// branded product cannot close its platform account from there either.
func TestDeleteOwnAccountProtectsPrimaryRoot(t *testing.T) {
	for _, tenantCode := range []string{"", "cafettonhouse"} {
		for _, test := range protectedTargets(primaryRootRequester())[:2] {
			t.Run(test.name+" tenant="+tenantCode, func(t *testing.T) {
				fixture := newAccountFixture(t, test.target)
				c, rec := accountContext(http.MethodDelete, "/users", test.target.CognitoSub, "", tenantCode)

				require.NoError(t, DeleteUser(c))

				assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "Protected account")
				fixture.repo.untouched(t, fixture.auth)
			})
		}
	}
}

func TestDeleteOwnAccountStillWorksForOtherAccounts(t *testing.T) {
	for name, account := range map[string]models.User{
		"operational root": {ID: uuid.Must(uuid.NewV4()), CognitoSub: "level-two", RootLevel: models.RootLevelOperational},
		"standard user":    {ID: uuid.Must(uuid.NewV4()), CognitoSub: "standard"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newAccountFixture(t, account)
			c, rec := accountContext(http.MethodDelete, "/users", account.CognitoSub, "", "")

			require.NoError(t, DeleteUser(c))

			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, 1, fixture.auth.deletes)
			assert.Equal(t, 1, fixture.repo.deletes)
		})
	}
}
