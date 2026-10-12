package users

import (
	"errors"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// membershipClientRepo implements ports.ClientRepository; only
// ListClientsByUser matters for the ADM-9 guards.
type membershipClientRepo struct {
	clients []models.Client
	err     error
}

func (m *membershipClientRepo) CreateClient(*models.Client) error               { return nil }
func (m *membershipClientRepo) GetClientByID(uuid.UUID) (*models.Client, error) { return nil, nil }
func (m *membershipClientRepo) UpdateClient(*models.Client) error               { return nil }
func (m *membershipClientRepo) DeleteClient(uuid.UUID) error                    { return nil }
func (m *membershipClientRepo) GetAllClients() ([]models.Client, error)         { return nil, nil }
func (m *membershipClientRepo) GetClientsByUser(uuid.UUID) ([]models.Client, error) {
	return nil, nil
}
func (m *membershipClientRepo) ListClientsPaginated(*uuid.UUID, dtos.ClientsListQuery) ([]models.Client, int64, error) {
	return nil, 0, nil
}
func (m *membershipClientRepo) GetChildrenClients(uuid.UUID) ([]models.Client, error) {
	return nil, nil
}
func (m *membershipClientRepo) CheckAccessRecursive(uuid.UUID, uuid.UUID) (bool, string) {
	return false, ""
}
func (m *membershipClientRepo) IsMember(uuid.UUID, uuid.UUID) (bool, string)           { return false, "" }
func (m *membershipClientRepo) AddMember(*models.ClientMember) error                   { return nil }
func (m *membershipClientRepo) RemoveMember(uuid.UUID, uuid.UUID) error                { return nil }
func (m *membershipClientRepo) UpdateMemberRole(uuid.UUID, uuid.UUID, uuid.UUID) error { return nil }
func (m *membershipClientRepo) GetMemberRole(uuid.UUID, uuid.UUID) (*models.ClientRole, error) {
	return nil, nil
}
func (m *membershipClientRepo) GetMembers(uuid.UUID) ([]models.ClientMember, error) { return nil, nil }
func (m *membershipClientRepo) DeleteAllMembers(uuid.UUID) error                    { return nil }
func (m *membershipClientRepo) ListClientsByUser(uuid.UUID) ([]models.Client, error) {
	return m.clients, m.err
}
func (m *membershipClientRepo) CountClientsByUsers([]uuid.UUID) (map[uuid.UUID]int64, error) {
	return map[uuid.UUID]int64{}, nil
}

type deleteCallLog struct {
	calls []string
}

func newDeleteFixture(target *models.User, clients []models.Client, localErr, cognitoDeleteErr error) (*AdminUserService, *deleteCallLog) {
	log := &deleteCallLog{}
	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			if id != target.ID {
				return nil, errors.New("record not found")
			}
			clone := *target
			return &clone, nil
		},
		DeleteUserFunc: func(uuid.UUID) error {
			log.calls = append(log.calls, "local-delete")
			return localErr
		},
		UpdateUserFunc: func(*models.User) error { return nil },
	}
	authRepo := &mockAuthRepo{
		SetUserEnabledFunc: func(_ string, enabled bool, _ string) error {
			if enabled {
				log.calls = append(log.calls, "cognito-enable")
			} else {
				log.calls = append(log.calls, "cognito-disable")
			}
			return nil
		},
		DeleteUserFunc: func(string, string) error {
			log.calls = append(log.calls, "cognito-delete")
			return cognitoDeleteErr
		},
	}
	return NewAdminUserService(userRepo, &membershipClientRepo{clients: clients}, authRepo), log
}

func plainUser() *models.User {
	return &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "target-sub", IsActive: true}
}

func TestAdminDeleteUser_RejectsSelf(t *testing.T) {
	target := plainUser()
	svc, log := newDeleteFixture(target, nil, nil, nil)

	err := svc.DeleteUser(target.ID, target.ID)

	require.ErrorIs(t, err, ErrAdminCannotTargetSelf)
	assert.Empty(t, log.calls)
}

func TestAdminDeleteUser_RejectsPrimaryRoot(t *testing.T) {
	for name, target := range map[string]*models.User{
		"root_level_1":   {ID: uuid.Must(uuid.NewV4()), CognitoSub: "r1", RootLevel: models.RootLevelPrimary},
		"legacy_is_root": {ID: uuid.Must(uuid.NewV4()), CognitoSub: "r1", IsRoot: true},
	} {
		t.Run(name, func(t *testing.T) {
			svc, log := newDeleteFixture(target, nil, nil, nil)
			err := svc.DeleteUser(uuid.Must(uuid.NewV4()), target.ID)
			require.ErrorIs(t, err, ErrAdminPrimaryRootProtected)
			assert.Empty(t, log.calls)
		})
	}
}

func TestAdminDeleteUser_AllowsOperationalRootTarget(t *testing.T) {
	target := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "r2", RootLevel: models.RootLevelOperational, IsActive: true}
	svc, log := newDeleteFixture(target, nil, nil, nil)

	require.NoError(t, svc.DeleteUser(uuid.Must(uuid.NewV4()), target.ID))
	assert.Equal(t, []string{"cognito-disable", "local-delete", "cognito-delete"}, log.calls)
}

func TestAdminDeleteUser_RejectsActiveMemberships(t *testing.T) {
	target := plainUser()
	svc, log := newDeleteFixture(target, []models.Client{{ID: uuid.Must(uuid.NewV4())}}, nil, nil)

	err := svc.DeleteUser(uuid.Must(uuid.NewV4()), target.ID)

	require.ErrorIs(t, err, ErrAdminUserHasActiveMembership)
	assert.Empty(t, log.calls)
}

func TestAdminDeleteUser_NotFound(t *testing.T) {
	svc, _ := newDeleteFixture(plainUser(), nil, nil, nil)
	err := svc.DeleteUser(uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()))
	require.ErrorIs(t, err, ErrAdminUserNotFound)
}

func TestAdminDeleteUser_LocalFailureReEnablesCognito(t *testing.T) {
	target := plainUser()
	svc, log := newDeleteFixture(target, nil, errors.New("db down"), nil)

	err := svc.DeleteUser(uuid.Must(uuid.NewV4()), target.ID)

	require.Error(t, err)
	assert.Equal(t, []string{"cognito-disable", "local-delete", "cognito-enable"}, log.calls,
		"cognito must never be deleted while the local user survives")
}

func TestAdminDeleteUser_LocalFailureKeepsInactiveUserDisabled(t *testing.T) {
	target := plainUser()
	target.IsActive = false
	svc, log := newDeleteFixture(target, nil, errors.New("db down"), nil)

	require.Error(t, svc.DeleteUser(uuid.Must(uuid.NewV4()), target.ID))
	assert.Equal(t, []string{"cognito-disable", "local-delete"}, log.calls)
}

func TestAdminDeleteUser_CognitoDeleteFailureStillSucceedsWithDisabledIdentity(t *testing.T) {
	target := plainUser()
	svc, log := newDeleteFixture(target, nil, nil, errors.New("cognito throttled"))

	require.NoError(t, svc.DeleteUser(uuid.Must(uuid.NewV4()), target.ID))
	assert.Equal(t, []string{"cognito-disable", "local-delete", "cognito-delete"}, log.calls)
}

func TestAdminDeactivateUser_Guards(t *testing.T) {
	target := plainUser()
	svc, log := newDeleteFixture(target, nil, nil, nil)

	_, err := svc.DeactivateUser(target.ID, target.ID)
	require.ErrorIs(t, err, ErrAdminCannotTargetSelf)

	root := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "r1", RootLevel: models.RootLevelPrimary, IsActive: true}
	rootSvc, rootLog := newDeleteFixture(root, nil, nil, nil)
	_, err = rootSvc.DeactivateUser(uuid.Must(uuid.NewV4()), root.ID)
	require.ErrorIs(t, err, ErrAdminPrimaryRootProtected)
	assert.Empty(t, rootLog.calls)
	assert.Empty(t, log.calls)

	user, err := svc.DeactivateUser(uuid.Must(uuid.NewV4()), target.ID)
	require.NoError(t, err)
	assert.False(t, user.IsActive)
	assert.Equal(t, []string{"cognito-disable"}, log.calls)
}
