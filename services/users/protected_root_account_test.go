package users

import (
	"errors"
	"events-stocks/models"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// accountCalls records every write that would remove or disable an account,
// locally or in the identity provider.
type accountCalls struct {
	localDeletes   int
	localUpdates   int
	providerDelete int
	providerEnable []bool
}

func accountRepos(target models.User, calls *accountCalls) (*mockUserRepo, *mockAuthRepo) {
	lookup := func() (*models.User, error) {
		copied := target
		return &copied, nil
	}
	users := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			if id != target.ID {
				return nil, gorm.ErrRecordNotFound
			}
			return lookup()
		},
		GetUserByCognitoSubFunc: func(sub string) (*models.User, error) {
			if sub != target.CognitoSub {
				return nil, gorm.ErrRecordNotFound
			}
			return lookup()
		},
		DeleteUserFunc: func(uuid.UUID) error { calls.localDeletes++; return nil },
		UpdateUserFunc: func(*models.User) error { calls.localUpdates++; return nil },
	}
	auth := &mockAuthRepo{
		DeleteUserFunc: func(string, string) error { calls.providerDelete++; return nil },
		SetUserEnabledFunc: func(_ string, enabled bool, _ string) error {
			calls.providerEnable = append(calls.providerEnable, enabled)
			return nil
		},
	}
	return users, auth
}

var primaryRootAccounts = []struct {
	name    string
	account models.User
}{
	{name: "legacy is_root account", account: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "legacy-root", IsRoot: true}},
	{name: "explicit level 1 account", account: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "level-one", RootLevel: models.RootLevelPrimary}},
	{name: "is_root with explicit level 1", account: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "both-flags", IsRoot: true, RootLevel: models.RootLevelPrimary}},
}

var removableAccounts = []struct {
	name    string
	account models.User
}{
	{name: "operational root", account: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "level-two", RootLevel: models.RootLevelOperational}},
	{name: "standard user", account: models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "standard"}},
}

// Level 1 cannot be granted through the API, so deleting or disabling a
// primary root could leave the platform without one. The admin endpoints
// are gated by RequirePrimaryRoot, so this also covers self-removal.
func TestAdminDeleteUserRefusesPrimaryRootAccounts(t *testing.T) {
	for _, test := range primaryRootAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			err := NewAdminUserService(users, &membershipClientRepo{}, auth).DeleteUser(uuid.Must(uuid.NewV4()), test.account.ID)

			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrProtectedRootAccount), "got %v", err)
			assert.Equal(t, accountCalls{}, *calls, "a protected account must not be touched")
		})
	}
}

func TestAdminDeleteUserStillDeletesOtherAccounts(t *testing.T) {
	for _, test := range removableAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			require.NoError(t, NewAdminUserService(users, &membershipClientRepo{}, auth).DeleteUser(uuid.Must(uuid.NewV4()), test.account.ID))

			assert.Equal(t, 1, calls.providerDelete)
			assert.Equal(t, 1, calls.localDeletes)
		})
	}
}

func TestAdminDeactivateRefusesPrimaryRootAccounts(t *testing.T) {
	for _, test := range primaryRootAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			_, err := NewAdminUserService(users, nil, auth).SetUserActive(test.account.ID, false)

			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrProtectedRootAccount), "got %v", err)
			assert.Equal(t, accountCalls{}, *calls, "a protected account must not be touched")
		})
	}
}

// Re-enabling stays possible so an already disabled primary root can be
// recovered.
func TestAdminActivateStillWorksForPrimaryRootAccounts(t *testing.T) {
	for _, test := range primaryRootAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			user, err := NewAdminUserService(users, nil, auth).SetUserActive(test.account.ID, true)

			require.NoError(t, err)
			assert.True(t, user.IsActive)
			assert.Equal(t, []bool{true}, calls.providerEnable)
		})
	}
}

func TestAdminDeactivateStillDisablesOtherAccounts(t *testing.T) {
	for _, test := range removableAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			user, err := NewAdminUserService(users, nil, auth).SetUserActive(test.account.ID, false)

			require.NoError(t, err)
			assert.False(t, user.IsActive)
			assert.Equal(t, []bool{false}, calls.providerEnable)
		})
	}
}

// DELETE /api/users closes the caller's own account; a primary root may not
// close its own, whatever product it is signed into.
func TestDeleteFullAccountRefusesPrimaryRootAccounts(t *testing.T) {
	for _, test := range primaryRootAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			err := newUserService(users, auth).DeleteFullAccount(test.account.CognitoSub)

			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrProtectedRootAccount), "got %v", err)
			assert.Equal(t, accountCalls{}, *calls, "a protected account must not be touched")
		})
	}
}

func TestDeleteFullAccountStillClosesOtherAccounts(t *testing.T) {
	for _, test := range removableAccounts {
		t.Run(test.name, func(t *testing.T) {
			calls := &accountCalls{}
			users, auth := accountRepos(test.account, calls)

			require.NoError(t, newUserService(users, auth).DeleteFullAccount(test.account.CognitoSub))

			assert.Equal(t, 1, calls.providerDelete)
			assert.Equal(t, 1, calls.localDeletes)
		})
	}
}
