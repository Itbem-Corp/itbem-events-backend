package clients

import (
	"errors"
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// memberTargetWorld is one organization with effective roles per user and
// the platform level of every known user account.
type memberTargetWorld struct {
	clientID uuid.UUID
	roles    map[uuid.UUID]string
	users    map[uuid.UUID]models.User
}

var seededHierarchy = map[string]int{
	"OWNER": 1, "ADMIN": 2, "EVENT_MANAGER": 3, "EDITOR": 4, "CHECKIN": 5, "ANALYST": 6, "MEMBER": 7, "GUEST": 8,
}

func newMemberTargetWorld() *memberTargetWorld {
	return &memberTargetWorld{clientID: uuid.Must(uuid.NewV4()), roles: map[uuid.UUID]string{}, users: map[uuid.UUID]models.User{}}
}

// member adds an account with the given effective role code ("" = no access).
func (w *memberTargetWorld) member(roleCode string, user models.User) uuid.UUID {
	user.ID = uuid.Must(uuid.NewV4())
	w.users[user.ID] = user
	if roleCode != "" {
		w.roles[user.ID] = roleCode
	}
	return user.ID
}

func (w *memberTargetWorld) service() *ClientService {
	return newClientService(&mockClientRepo{
		CheckAccessRecursiveFunc: func(userID, clientID uuid.UUID) (bool, string) {
			if clientID != w.clientID {
				return false, ""
			}
			role, ok := w.roles[userID]
			return ok, role
		},
	}, &mockClientRoleRepo{
		GetByCodeFunc: func(code string) (*models.ClientRole, error) {
			hierarchy, ok := seededHierarchy[strings.ToUpper(code)]
			if !ok {
				return nil, gorm.ErrRecordNotFound
			}
			return &models.ClientRole{Code: code, Hierarchy: hierarchy}, nil
		},
	}, nil)
}

func (w *memberTargetWorld) lookup(id uuid.UUID) (*models.User, error) {
	user, ok := w.users[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return &user, nil
}

func (w *memberTargetWorld) authorize(requesterID, targetID uuid.UUID) error {
	requester := w.users[requesterID]
	return w.service().AuthorizeMemberTarget(&requester, w.clientID, targetID, w.lookup)
}

func TestAuthorizeMemberTargetRanksOrganizationManagers(t *testing.T) {
	world := newMemberTargetWorld()
	owner := world.member("OWNER", models.User{})
	coOwner := world.member("OWNER", models.User{})
	admin := world.member("ADMIN", models.User{})
	coAdmin := world.member("ADMIN", models.User{})
	inheritedAdmin := world.member("INHERITED_ADMIN", models.User{})
	inheritedOwner := world.member("INHERITED_OWNER", models.User{})
	editor := world.member("EDITOR", models.User{})
	viewer := world.member("Guest", models.User{})

	cases := []struct {
		name      string
		requester uuid.UUID
		target    uuid.UUID
		allowed   bool
	}{
		{name: "owner acts on admin", requester: owner, target: admin, allowed: true},
		{name: "owner acts on editor", requester: owner, target: editor, allowed: true},
		{name: "owner acts on inherited admin", requester: owner, target: inheritedAdmin, allowed: true},
		{name: "owner cannot act on another owner", requester: owner, target: coOwner},
		{name: "owner cannot act on an inherited owner", requester: owner, target: inheritedOwner},
		{name: "owner cannot act on itself", requester: owner, target: owner},
		{name: "admin acts on editor", requester: admin, target: editor, allowed: true},
		{name: "admin acts on viewer", requester: admin, target: viewer, allowed: true},
		{name: "admin cannot act on owner", requester: admin, target: owner},
		{name: "admin cannot act on inherited owner", requester: admin, target: inheritedOwner},
		{name: "admin cannot act on another admin", requester: admin, target: coAdmin},
		{name: "admin cannot act on itself", requester: admin, target: admin},
		{name: "inherited admin acts on editor", requester: inheritedAdmin, target: editor, allowed: true},
		{name: "inherited admin cannot act on a direct admin", requester: inheritedAdmin, target: admin},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := world.authorize(test.requester, test.target)
			if test.allowed {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrMemberTargetDenied), "got %v", err)
		})
	}
}

func TestAuthorizeMemberTargetProtectsPlatformRootAccounts(t *testing.T) {
	world := newMemberTargetWorld()
	owner := world.member("OWNER", models.User{})
	operational := world.member("", models.User{RootLevel: models.RootLevelOperational})
	targets := map[string]uuid.UUID{
		"primary root with a viewer membership":     world.member("Guest", models.User{RootLevel: models.RootLevelPrimary}),
		"legacy is_root with a viewer membership":   world.member("Guest", models.User{IsRoot: true}),
		"operational root with a viewer membership": world.member("Guest", models.User{RootLevel: models.RootLevelOperational}),
	}
	for name, target := range targets {
		for requesterName, requester := range map[string]uuid.UUID{"owner": owner, "operational root": operational} {
			t.Run(requesterName+" on "+name, func(t *testing.T) {
				err := world.authorize(requester, target)

				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrMemberTargetDenied), "got %v", err)
			})
		}
	}
}

func TestAuthorizeMemberTargetOperationalRootStopsAtOwner(t *testing.T) {
	world := newMemberTargetWorld()
	operational := world.member("", models.User{RootLevel: models.RootLevelOperational})
	legacyOperational := world.member("", models.User{IsRoot: true, RootLevel: models.RootLevelOperational})

	for _, requester := range []uuid.UUID{operational, legacyOperational} {
		for _, role := range []string{"ADMIN", "INHERITED_ADMIN", "EDITOR", "Guest"} {
			target := world.member(role, models.User{})
			require.NoError(t, world.authorize(requester, target), role)
		}
		require.NoError(t, world.authorize(requester, world.member("", models.User{})), "a target without access has no rank")
		for _, role := range []string{"OWNER", "INHERITED_OWNER"} {
			err := world.authorize(requester, world.member(role, models.User{}))
			require.Error(t, err, role)
			assert.True(t, errors.Is(err, ErrMemberTargetDenied), "got %v", err)
		}
	}
}

func TestAuthorizeMemberTargetPrimaryRootActsOnAnyone(t *testing.T) {
	world := newMemberTargetWorld()
	for _, primary := range []models.User{{IsRoot: true}, {RootLevel: models.RootLevelPrimary}} {
		requester := world.member("", primary)
		for _, target := range []uuid.UUID{
			world.member("OWNER", models.User{}),
			world.member("ADMIN", models.User{}),
			world.member("Guest", models.User{RootLevel: models.RootLevelPrimary}),
			world.member("Guest", models.User{RootLevel: models.RootLevelOperational}),
			requester,
		} {
			require.NoError(t, world.authorize(requester, target))
		}
	}
}

// A missing user row is not a platform root and has no organization rank;
// the caller's own validation (membership checks) still applies.
func TestAuthorizeMemberTargetTreatsUnknownUsersAsUnranked(t *testing.T) {
	world := newMemberTargetWorld()
	admin := world.member("ADMIN", models.User{})

	require.NoError(t, world.authorize(admin, uuid.Must(uuid.NewV4())))
}

func TestAuthorizeMemberTargetFailsClosed(t *testing.T) {
	world := newMemberTargetWorld()
	admin := world.member("ADMIN", models.User{})
	editor := world.member("EDITOR", models.User{})
	outsider := world.member("", models.User{})
	requester := world.users[admin]

	t.Run("user lookup failure", func(t *testing.T) {
		err := world.service().AuthorizeMemberTarget(&requester, world.clientID, editor, func(uuid.UUID) (*models.User, error) {
			return nil, errors.New("database unavailable")
		})
		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrMemberTargetDenied), "infrastructure errors are not denials")
	})
	t.Run("no user lookup", func(t *testing.T) {
		require.Error(t, world.service().AuthorizeMemberTarget(&requester, world.clientID, editor, nil))
	})
	t.Run("unknown target role", func(t *testing.T) {
		world.roles[editor] = "CUSTOM_ROLE"
		t.Cleanup(func() { world.roles[editor] = "EDITOR" })
		require.Error(t, world.authorize(admin, editor))
	})
	t.Run("requester without a managing role", func(t *testing.T) {
		err := world.authorize(outsider, editor)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrMemberTargetDenied), "got %v", err)
	})
	t.Run("no requester", func(t *testing.T) {
		err := world.service().AuthorizeMemberTarget(nil, world.clientID, editor, world.lookup)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrMemberTargetDenied), "got %v", err)
	})
}

func TestPackageAuthorizeMemberTargetRequiresConfiguredService(t *testing.T) {
	previous := _clientSvc
	t.Cleanup(func() { _clientSvc = previous })
	_clientSvc = nil

	err := AuthorizeMemberTarget(&models.User{ID: uuid.Must(uuid.NewV4())}, uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), func(uuid.UUID) (*models.User, error) {
		return nil, gorm.ErrRecordNotFound
	})

	require.Error(t, err)
}
