package users

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockUserRepo struct {
	CreateUserFunc            func(user *models.User) error
	UpdateUserFunc            func(user *models.User) error
	DeleteUserFunc            func(id uuid.UUID) error
	GetUserByIDFunc           func(id uuid.UUID) (*models.User, error)
	GetUserByCognitoSubFunc   func(sub string) (*models.User, error)
	GetUserByEmailFunc        func(email string) (*models.User, error)
	UpdateUserFieldsFunc      func(userID uuid.UUID, fields map[string]interface{}) error
	ClearProfileImageFunc     func(userID uuid.UUID) error
	SetUserRootFunc           func(userID uuid.UUID, isRoot bool) error
	ListAllUsersFunc          func() ([]models.User, error)
	ListAllUsersPaginatedFunc func(query dtos.AdminUsersListQuery) ([]models.User, int64, error)
	SetUserActiveFunc         func(userID uuid.UUID, active bool) error
}

func (m *mockUserRepo) CreateUser(user *models.User) error {
	if m.CreateUserFunc != nil {
		return m.CreateUserFunc(user)
	}
	return nil
}
func (m *mockUserRepo) UpdateUser(user *models.User) error {
	if m.UpdateUserFunc != nil {
		return m.UpdateUserFunc(user)
	}
	return nil
}
func (m *mockUserRepo) DeleteUser(id uuid.UUID) error {
	if m.DeleteUserFunc != nil {
		return m.DeleteUserFunc(id)
	}
	return nil
}
func (m *mockUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	if m.GetUserByIDFunc != nil {
		return m.GetUserByIDFunc(id)
	}
	return nil, errors.New("not found")
}
func (m *mockUserRepo) GetUserByCognitoSub(sub string) (*models.User, error) {
	if m.GetUserByCognitoSubFunc != nil {
		return m.GetUserByCognitoSubFunc(sub)
	}
	return nil, errors.New("record not found")
}
func (m *mockUserRepo) GetUserByEmail(email string) (*models.User, error) {
	if m.GetUserByEmailFunc != nil {
		return m.GetUserByEmailFunc(email)
	}
	return nil, errors.New("record not found")
}
func (m *mockUserRepo) UpdateUserFields(userID uuid.UUID, fields map[string]interface{}) error {
	if m.UpdateUserFieldsFunc != nil {
		return m.UpdateUserFieldsFunc(userID, fields)
	}
	return nil
}
func (m *mockUserRepo) ClearProfileImage(userID uuid.UUID) error {
	if m.ClearProfileImageFunc != nil {
		return m.ClearProfileImageFunc(userID)
	}
	return nil
}
func (m *mockUserRepo) SetUserRoot(userID uuid.UUID, isRoot bool) error {
	if m.SetUserRootFunc != nil {
		return m.SetUserRootFunc(userID, isRoot)
	}
	return nil
}
func (m *mockUserRepo) ListAllUsers() ([]models.User, error) {
	if m.ListAllUsersFunc != nil {
		return m.ListAllUsersFunc()
	}
	return nil, nil
}
func (m *mockUserRepo) ListAllUsersPaginated(query dtos.AdminUsersListQuery) ([]models.User, int64, error) {
	if m.ListAllUsersPaginatedFunc != nil {
		return m.ListAllUsersPaginatedFunc(query)
	}
	return nil, 0, nil
}
func (m *mockUserRepo) SetUserActive(userID uuid.UUID, active bool) error {
	if m.SetUserActiveFunc != nil {
		return m.SetUserActiveFunc(userID, active)
	}
	return nil
}

var _ ports.UserRepository = (*mockUserRepo)(nil)

// ---------------------------------------------------------------------------

type mockAuthRepo struct {
	GetUserFunc        func(sub string, provider string) (*dtos.AuthUser, error)
	UpdateUserFunc     func(sub string, attrs map[string]string, provider string) error
	DeleteUserFunc     func(sub string, provider string) error
	CreateUserFunc     func(req dtos.CreateAuthUserRequest, provider string) (*dtos.AuthUser, error)
	SetUserEnabledFunc func(sub string, enabled bool, provider string) error
	InviteUserFunc     func(email, firstName, lastName, provider string) (*dtos.AuthUser, error)
}

func (m *mockAuthRepo) GetUser(sub string, provider string) (*dtos.AuthUser, error) {
	if m.GetUserFunc != nil {
		return m.GetUserFunc(sub, provider)
	}
	return nil, errors.New("not found")
}
func (m *mockAuthRepo) UpdateUser(sub string, attrs map[string]string, provider string) error {
	if m.UpdateUserFunc != nil {
		return m.UpdateUserFunc(sub, attrs, provider)
	}
	return nil
}
func (m *mockAuthRepo) DeleteUser(sub string, provider string) error {
	if m.DeleteUserFunc != nil {
		return m.DeleteUserFunc(sub, provider)
	}
	return nil
}
func (m *mockAuthRepo) CreateUser(req dtos.CreateAuthUserRequest, provider string) (*dtos.AuthUser, error) {
	if m.CreateUserFunc != nil {
		return m.CreateUserFunc(req, provider)
	}
	return &dtos.AuthUser{Sub: "new-sub", Email: req.Email}, nil
}
func (m *mockAuthRepo) SetUserEnabled(sub string, enabled bool, provider string) error {
	if m.SetUserEnabledFunc != nil {
		return m.SetUserEnabledFunc(sub, enabled, provider)
	}
	return nil
}
func (m *mockAuthRepo) InviteUser(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
	if m.InviteUserFunc != nil {
		return m.InviteUserFunc(email, firstName, lastName, provider)
	}
	return nil, errors.New("not implemented")
}

var _ ports.AuthProviderRepository = (*mockAuthRepo)(nil)

type tenantAwareMockAuthRepo struct {
	*mockAuthRepo
	InviteUserForTenantFunc func(email, firstName, lastName, tenantCode, provider string) (*dtos.AuthUser, error)
}

func (m *tenantAwareMockAuthRepo) InviteUserForTenant(email, firstName, lastName, tenantCode, provider string) (*dtos.AuthUser, error) {
	return m.InviteUserForTenantFunc(email, firstName, lastName, tenantCode, provider)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newUserService(ur *mockUserRepo, ar *mockAuthRepo) *UserService {
	if ur == nil {
		ur = &mockUserRepo{}
	}
	if ar == nil {
		ar = &mockAuthRepo{}
	}
	// cfg is nil — safe as long as tests don't trigger S3 avatar deletion
	return NewUserService(ur, ar, nil)
}

// ---------------------------------------------------------------------------
// SyncUser tests
// ---------------------------------------------------------------------------

func TestSyncUser_CreatesNewUser(t *testing.T) {
	// Scenario: user exists in auth provider but not in local DB (first login).
	sub := "cognito-sub-123"

	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{
				Sub:       sub,
				Email:     "alice@example.com",
				FirstName: "Alice",
				LastName:  "Smith",
				IsActive:  true,
			}, nil
		},
	}

	var createdUser *models.User
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return nil, errors.New("record not found") // not in DB yet
		},
		CreateUserFunc: func(u *models.User) error {
			createdUser = u
			return nil
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.SyncUser(sub)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "alice@example.com", result.Email)
	assert.Equal(t, "Alice", result.FirstName)
	assert.Equal(t, "Smith", result.LastName)
	assert.True(t, result.IsActive)

	// Verify CreateUser was called
	require.NotNil(t, createdUser)
	assert.Equal(t, sub, createdUser.CognitoSub)
}

func TestSyncUser_ReturnsExistingUser_NoUpdate(t *testing.T) {
	// Scenario: user exists in DB and all fields match auth provider — no update.
	sub := "cognito-sub-456"
	userID := uuid.Must(uuid.NewV4())

	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{
				Sub:       sub,
				Email:     "bob@example.com",
				FirstName: "Bob",
				LastName:  "Jones",
			}, nil
		},
	}

	updateFieldsCalled := false
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return &models.User{
				ID:         userID,
				CognitoSub: sub,
				Email:      "bob@example.com", // same as auth provider
				FirstName:  "Bob",
				LastName:   "Jones",
			}, nil
		},
		UpdateUserFieldsFunc: func(uID uuid.UUID, fields map[string]interface{}) error {
			updateFieldsCalled = true
			return nil
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.SyncUser(sub)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, userID, result.ID)
	assert.False(t, updateFieldsCalled, "UpdateUserFields must NOT be called when data is clean")
}

func TestSyncUser_ReusesRecentProviderSync(t *testing.T) {
	sub := "cognito-sub-cached"
	userID := uuid.Must(uuid.NewV4())

	providerCalls := 0
	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			providerCalls++
			return &dtos.AuthUser{
				Sub:       sub,
				Email:     "cached@example.com",
				FirstName: "Cached",
				LastName:  "User",
			}, nil
		},
	}

	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return &models.User{
				ID:         userID,
				CognitoSub: sub,
				Email:      "cached@example.com",
				FirstName:  "Cached",
				LastName:   "User",
			}, nil
		},
	}

	svc := newUserService(userRepo, authRepo)
	first, err := svc.SyncUser(sub)
	require.NoError(t, err)
	second, err := svc.SyncUser(sub)
	require.NoError(t, err)

	assert.Equal(t, userID, first.ID)
	assert.Equal(t, userID, second.ID)
	assert.Equal(t, 1, providerCalls, "provider sync should be reused inside the freshness window")
}

func TestSyncUser_UpdatesDirtyEmail(t *testing.T) {
	// Scenario: email changed in auth provider — local record must be synced.
	sub := "cognito-sub-789"
	userID := uuid.Must(uuid.NewV4())

	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{
				Sub:       sub,
				Email:     "carol.new@example.com", // new email
				FirstName: "Carol",
				LastName:  "White",
			}, nil
		},
	}

	var capturedFields map[string]interface{}
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return &models.User{
				ID:         userID,
				CognitoSub: sub,
				Email:      "carol.old@example.com", // stale email
				FirstName:  "Carol",
				LastName:   "White",
			}, nil
		},
		UpdateUserFieldsFunc: func(uID uuid.UUID, fields map[string]interface{}) error {
			capturedFields = fields
			return nil
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.SyncUser(sub)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "carol.new@example.com", result.Email)

	require.NotNil(t, capturedFields)
	assert.Equal(t, "carol.new@example.com", capturedFields["email"])
}

func TestSyncUser_UpdatesDirtyName(t *testing.T) {
	// Scenario: first name and last name changed in auth provider.
	sub := "cognito-sub-abc"
	userID := uuid.Must(uuid.NewV4())

	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{
				Sub:       sub,
				Email:     "dave@example.com",
				FirstName: "David", // changed from "Dave"
				LastName:  "Brown", // changed from "Browne"
			}, nil
		},
	}

	var capturedFields map[string]interface{}
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return &models.User{
				ID:         userID,
				CognitoSub: sub,
				Email:      "dave@example.com",
				FirstName:  "Dave",
				LastName:   "Browne",
			}, nil
		},
		UpdateUserFieldsFunc: func(uID uuid.UUID, fields map[string]interface{}) error {
			capturedFields = fields
			return nil
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.SyncUser(sub)

	require.NoError(t, err)
	assert.Equal(t, "David", result.FirstName)
	assert.Equal(t, "Brown", result.LastName)

	require.NotNil(t, capturedFields)
	assert.Equal(t, "David", capturedFields["first_name"])
	assert.Equal(t, "Brown", capturedFields["last_name"])
}

func TestSyncUser_AuthProviderError(t *testing.T) {
	// Scenario: auth provider (Cognito) is unreachable or user does not exist there.
	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return nil, errors.New("UserNotFoundException: user does not exist")
		},
	}

	svc := newUserService(nil, authRepo)
	result, err := svc.SyncUser("ghost-sub")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "UserNotFoundException")
	assert.Nil(t, result)
}

func TestSyncUser_UsesExistingLocalUserOnlyWithExplicitLocalFallback(t *testing.T) {
	t.Setenv("ENV", "local")
	local := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "known-sub", Email: "known@example.com", IsActive: true}
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) { return local, nil },
	}
	authRepo := &mockAuthRepo{
		GetUserFunc: func(string, string) (*dtos.AuthUser, error) { return nil, errors.New("credentials unavailable") },
	}

	svc := NewUserService(userRepo, authRepo, &models.Config{AllowLocalUserSyncFallback: "true"})
	result, err := svc.SyncUser(local.CognitoSub)

	require.NoError(t, err)
	assert.Same(t, local, result)
}

func TestSyncUser_DoesNotUseLocalFallbackOutsideExplicitLocalMode(t *testing.T) {
	t.Setenv("ENV", "production")
	local := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "known-sub", Email: "known@example.com", IsActive: true}
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) { return local, nil },
	}
	authRepo := &mockAuthRepo{
		GetUserFunc: func(string, string) (*dtos.AuthUser, error) { return nil, errors.New("credentials unavailable") },
	}

	svc := NewUserService(userRepo, authRepo, &models.Config{AllowLocalUserSyncFallback: "true"})
	result, err := svc.SyncUser(local.CognitoSub)

	require.Error(t, err)
	assert.Nil(t, result)
}

func TestSyncUser_GrantsConfiguredLocalBootstrapRootOnFirstLocalSync(t *testing.T) {
	t.Setenv("ENV", "local")
	const email = "admin@example.com"
	var created *models.User
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) { return nil, errors.New("record not found") },
		CreateUserFunc: func(user *models.User) error {
			created = user
			return nil
		},
	}
	authRepo := &mockAuthRepo{GetUserFunc: func(sub, provider string) (*dtos.AuthUser, error) {
		return &dtos.AuthUser{Sub: sub, Email: email, FirstName: "Local", LastName: "Admin", IsActive: true}, nil
	}}

	svc := NewUserService(userRepo, authRepo, &models.Config{LocalBootstrapRootEmails: "other@example.com, ADMIN@example.com"})
	result, err := svc.SyncUser("admin-sub")

	require.NoError(t, err)
	require.Same(t, created, result)
	assert.True(t, result.IsRoot)
	assert.Equal(t, models.RootLevelPrimary, result.RootLevel)
}

func TestSyncUser_LocalBootstrapRootIsIgnoredOutsideLocalEnvironment(t *testing.T) {
	t.Setenv("ENV", "production")
	var created *models.User
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) { return nil, errors.New("record not found") },
		CreateUserFunc: func(user *models.User) error {
			created = user
			return nil
		},
	}
	authRepo := &mockAuthRepo{GetUserFunc: func(sub, provider string) (*dtos.AuthUser, error) {
		return &dtos.AuthUser{Sub: sub, Email: "admin@example.com", IsActive: true}, nil
	}}

	svc := NewUserService(userRepo, authRepo, &models.Config{LocalBootstrapRootEmails: "admin@example.com"})
	result, err := svc.SyncUser("admin-sub")

	require.NoError(t, err)
	require.Same(t, created, result)
	assert.False(t, result.IsRoot)
	assert.Equal(t, models.RootLevelNone, result.RootLevel)
}

func TestSyncUser_PromotesExistingConfiguredLocalBootstrapRoot(t *testing.T) {
	t.Setenv("ENV", "local")
	local := &models.User{ID: uuid.Must(uuid.NewV4()), CognitoSub: "known-sub", Email: "admin@example.com", IsActive: true}
	var fields map[string]interface{}
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) { return local, nil },
		UpdateUserFieldsFunc: func(_ uuid.UUID, received map[string]interface{}) error {
			fields = received
			return nil
		},
	}
	authRepo := &mockAuthRepo{GetUserFunc: func(sub, provider string) (*dtos.AuthUser, error) {
		return &dtos.AuthUser{Sub: sub, Email: "admin@example.com", IsActive: true}, nil
	}}

	svc := NewUserService(userRepo, authRepo, &models.Config{LocalBootstrapRootEmails: "admin@example.com"})
	result, err := svc.SyncUser(local.CognitoSub)

	require.NoError(t, err)
	assert.True(t, result.IsPrimaryRoot())
	require.NotNil(t, fields)
	assert.Equal(t, true, fields["is_root"])
	assert.Equal(t, models.RootLevelPrimary, fields["root_level"])
}

func TestBootstrapConfiguredLocalRootCreatesOnlyAllowListedLocalIdentity(t *testing.T) {
	t.Setenv("ENV", "local")
	var created *models.User
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) { return nil, errors.New("record not found") },
		CreateUserFunc: func(user *models.User) error {
			created = user
			return nil
		},
	}
	svc := NewUserService(userRepo, &mockAuthRepo{}, &models.Config{LocalBootstrapRootEmails: "admin@example.com"})

	result, err := svc.BootstrapConfiguredLocalRoot("trusted-sub", "ADMIN@example.com")

	require.NoError(t, err)
	require.Same(t, created, result)
	assert.True(t, result.IsPrimaryRoot())
	assert.True(t, result.IsActive)

	_, err = svc.BootstrapConfiguredLocalRoot("untrusted-sub", "other@example.com")
	require.Error(t, err)
}

func TestBootstrapConfiguredLocalRootRecoversConcurrentCreate(t *testing.T) {
	t.Setenv("ENV", "local")
	existing := &models.User{
		ID: uuid.Must(uuid.NewV4()), CognitoSub: "trusted-sub", Email: "admin@example.com",
		IsActive: true, IsRoot: true, RootLevel: models.RootLevelPrimary,
	}
	lookups := 0
	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(string) (*models.User, error) {
			lookups++
			if lookups == 1 {
				return nil, errors.New("record not found")
			}
			return existing, nil
		},
		CreateUserFunc: func(*models.User) error {
			return errors.New("duplicate key value violates unique constraint")
		},
	}
	svc := NewUserService(userRepo, &mockAuthRepo{}, &models.Config{LocalBootstrapRootEmails: "admin@example.com"})

	result, err := svc.BootstrapConfiguredLocalRoot("trusted-sub", "ADMIN@example.com")

	require.NoError(t, err)
	require.Same(t, existing, result)
	assert.Equal(t, 2, lookups)
}

func TestBootstrapConfiguredLocalRootFailsClosedOutsideLocalEnvironment(t *testing.T) {
	t.Setenv("ENV", "production")
	created := false
	userRepo := &mockUserRepo{CreateUserFunc: func(*models.User) error {
		created = true
		return nil
	}}
	svc := NewUserService(userRepo, &mockAuthRepo{}, &models.Config{LocalBootstrapRootEmails: "admin@example.com"})

	_, err := svc.BootstrapConfiguredLocalRoot("trusted-sub", "admin@example.com")

	require.Error(t, err)
	assert.False(t, created)
}

func TestSyncUser_DBError_NonRecordNotFound(t *testing.T) {
	// Scenario: DB returns a real error (not "record not found") — must propagate.
	sub := "cognito-sub-err"

	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{Sub: sub, Email: "err@example.com"}, nil
		},
	}

	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return nil, errors.New("connection refused") // real DB error
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.SyncUser(sub)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
	assert.Nil(t, result)
}

func TestSyncUser_CreateUserFails(t *testing.T) {
	// Scenario: user not in DB but CreateUser fails.
	sub := "cognito-sub-new-fail"

	authRepo := &mockAuthRepo{
		GetUserFunc: func(s, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{Sub: sub, Email: "fail@example.com", FirstName: "Fail", LastName: "User"}, nil
		},
	}

	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return nil, errors.New("record not found")
		},
		CreateUserFunc: func(u *models.User) error {
			return errors.New("duplicate key constraint")
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.SyncUser(sub)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate key constraint")
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// UpdateUserInformation tests
// ---------------------------------------------------------------------------

func TestUpdateUserInformation_EmptyFirstName(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: "sub"}, nil
		},
	}

	svc := newUserService(userRepo, nil)
	result, err := svc.UpdateUserInformation(userID, "", "Smith")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requeridos")
	assert.Nil(t, result)
}

func TestUpdateUserInformation_EmptyLastName(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: "sub"}, nil
		},
	}

	svc := newUserService(userRepo, nil)
	result, err := svc.UpdateUserInformation(userID, "Alice", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "requeridos")
	assert.Nil(t, result)
}

func TestUpdateUserInformation_Success(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	authRepo := &mockAuthRepo{
		UpdateUserFunc: func(sub string, attrs map[string]string, provider string) error {
			return nil
		},
	}
	var savedUser *models.User
	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: "sub", FirstName: "Old", LastName: "Name"}, nil
		},
		UpdateUserFunc: func(u *models.User) error {
			savedUser = u
			return nil
		},
	}

	svc := newUserService(userRepo, authRepo)
	result, err := svc.UpdateUserInformation(userID, "Alice", "Smith")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "Alice", result.FirstName)
	assert.Equal(t, "Smith", result.LastName)

	require.NotNil(t, savedUser)
	assert.Equal(t, "Alice", savedUser.FirstName)
}

func TestAdminListAllUsers_NormalizesQueryAndReturnsPage(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	var gotQuery dtos.AdminUsersListQuery
	userRepo := &mockUserRepo{
		ListAllUsersPaginatedFunc: func(query dtos.AdminUsersListQuery) ([]models.User, int64, error) {
			gotQuery = query
			return []models.User{
				{
					ID:        userID,
					Email:     "ana@example.com",
					FirstName: "Ana",
					LastName:  "Lopez",
					IsActive:  true,
				},
			}, 1, nil
		},
	}

	svc := NewAdminUserService(userRepo, nil, nil)
	result, err := svc.ListAllUsers(dtos.AdminUsersListQuery{
		Page:     -3,
		PageSize: 500,
		Search:   "  Ana  ",
		Status:   "ACTIVE",
	})

	require.NoError(t, err)
	assert.Equal(t, dtos.AdminUsersListQuery{Page: 1, PageSize: 50, Search: "Ana", Status: "active"}, gotQuery)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, 1, result.Page)
	assert.Equal(t, 50, result.PageSize)
	assert.Equal(t, 1, result.TotalPages)
	require.Len(t, result.Data, 1)
	assert.Equal(t, userID, result.Data[0].ID)
}

func TestAdminListAllUsers_PreservesNonRootScope(t *testing.T) {
	var gotQuery dtos.AdminUsersListQuery
	userRepo := &mockUserRepo{
		ListAllUsersPaginatedFunc: func(query dtos.AdminUsersListQuery) ([]models.User, int64, error) {
			gotQuery = query
			return nil, 0, nil
		},
	}

	_, err := NewAdminUserService(userRepo, nil, nil).ListAllUsers(dtos.AdminUsersListQuery{Status: "non_root"})
	require.NoError(t, err)
	assert.Equal(t, "non_root", gotQuery.Status)
}

func TestAdminUpdateUserInformation_Success(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	var cognitoSub string
	var cognitoAttrs map[string]string
	authRepo := &mockAuthRepo{
		UpdateUserFunc: func(sub string, attrs map[string]string, provider string) error {
			cognitoSub = sub
			cognitoAttrs = attrs
			return nil
		},
	}

	var savedUser *models.User
	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: "sub", FirstName: "Old", LastName: "Name"}, nil
		},
		UpdateUserFunc: func(u *models.User) error {
			savedUser = u
			return nil
		},
	}

	svc := NewAdminUserService(userRepo, nil, authRepo)
	result, err := svc.UpdateUserInformation(userID, "Alice", "Smith")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "sub", cognitoSub)
	assert.Equal(t, map[string]string{"given_name": "Alice", "family_name": "Smith"}, cognitoAttrs)
	require.NotNil(t, savedUser)
	assert.Equal(t, "Alice", savedUser.FirstName)
	assert.Equal(t, "Smith", savedUser.LastName)
}

func TestAdminSetUserActive_ReturnsUpdatedUser(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	var cognitoEnabled bool
	authRepo := &mockAuthRepo{
		SetUserEnabledFunc: func(sub string, enabled bool, provider string) error {
			assert.Equal(t, "cognito-sub", sub)
			assert.Equal(t, "cognito", provider)
			cognitoEnabled = enabled
			return nil
		},
	}

	var savedUser *models.User
	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			assert.Equal(t, userID, id)
			return &models.User{
				ID:         userID,
				CognitoSub: "cognito-sub",
				Email:      "ana@example.com",
				FirstName:  "Ana",
				LastName:   "Lopez",
				IsActive:   false,
			}, nil
		},
		UpdateUserFunc: func(user *models.User) error {
			savedUser = user
			return nil
		},
	}

	svc := NewAdminUserService(userRepo, nil, authRepo)
	result, err := svc.SetUserActive(userID, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, savedUser)
	assert.True(t, cognitoEnabled)
	assert.True(t, result.IsActive)
	assert.True(t, savedUser.IsActive)
	assert.Equal(t, "ana@example.com", result.Email)
}

func TestAdminDeleteUser_Success(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	var deletedSub string
	authRepo := &mockAuthRepo{
		DeleteUserFunc: func(sub string, provider string) error {
			deletedSub = sub
			return nil
		},
	}

	var deletedID uuid.UUID
	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: "sub"}, nil
		},
		DeleteUserFunc: func(id uuid.UUID) error {
			deletedID = id
			return nil
		},
	}

	svc := NewAdminUserService(userRepo, nil, authRepo)
	err := svc.DeleteUser(userID)

	require.NoError(t, err)
	assert.Equal(t, "sub", deletedSub)
	assert.Equal(t, userID, deletedID)
}

// ---------------------------------------------------------------------------
// DeleteFullAccount tests
// ---------------------------------------------------------------------------

func TestDeleteFullAccount_Success(t *testing.T) {
	sub := "cognito-sub-del"
	userID := uuid.Must(uuid.NewV4())

	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: sub, ProfileImage: ""}, nil
		},
		DeleteUserFunc: func(id uuid.UUID) error { return nil },
	}
	authRepo := &mockAuthRepo{
		DeleteUserFunc: func(sub, provider string) error { return nil },
	}

	svc := newUserService(userRepo, authRepo)
	err := svc.DeleteFullAccount(sub)
	require.NoError(t, err)
}

func TestDeleteFullAccount_AuthError(t *testing.T) {
	sub := "cognito-sub-fail"
	userID := uuid.Must(uuid.NewV4())

	userRepo := &mockUserRepo{
		GetUserByCognitoSubFunc: func(s string) (*models.User, error) {
			return &models.User{ID: userID, CognitoSub: sub}, nil
		},
	}
	authRepo := &mockAuthRepo{
		DeleteUserFunc: func(sub, provider string) error {
			return errors.New("cognito delete failed")
		},
	}

	svc := newUserService(userRepo, authRepo)
	err := svc.DeleteFullAccount(sub)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// UpdateProfileImage tests
// ---------------------------------------------------------------------------

func TestUpdateProfileImage_Success(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	newPath := "avatars/user123/photo.jpg"

	var savedUser *models.User
	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return &models.User{ID: userID, ProfileImage: ""}, nil
		},
		UpdateUserFunc: func(u *models.User) error {
			savedUser = u
			return nil
		},
	}

	svc := newUserService(userRepo, nil)
	err := svc.UpdateProfileImage(userID, newPath)
	require.NoError(t, err)
	require.NotNil(t, savedUser)
	assert.Equal(t, newPath, savedUser.ProfileImage)
}

func TestUpdateProfileImage_RepoError(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())

	userRepo := &mockUserRepo{
		GetUserByIDFunc: func(id uuid.UUID) (*models.User, error) {
			return nil, errors.New("db error")
		},
	}

	svc := newUserService(userRepo, nil)
	err := svc.UpdateProfileImage(userID, "some/path.jpg")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// SetUserRoot test
// ---------------------------------------------------------------------------

func TestSetUserRoot_Success(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	var capturedIsRoot bool

	userRepo := &mockUserRepo{
		SetUserRootFunc: func(id uuid.UUID, isRoot bool) error {
			capturedIsRoot = isRoot
			return nil
		},
	}

	svc := newUserService(userRepo, nil)
	err := svc.SetUserRoot(userID, true)
	require.NoError(t, err)
	assert.True(t, capturedIsRoot)
}

func TestInviteUser_UsesCognitoSubjectAndNormalizesIdentity(t *testing.T) {
	var invitedEmail, invitedFirstName, invitedLastName string
	var created *models.User
	authRepo := &mockAuthRepo{
		InviteUserFunc: func(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
			invitedEmail, invitedFirstName, invitedLastName = email, firstName, lastName
			assert.Equal(t, "cognito", provider)
			return &dtos.AuthUser{
				Sub: "cognito-sub-immutable", Email: email,
				FirstName: firstName, LastName: lastName, IsActive: true,
			}, nil
		},
	}
	userRepo := &mockUserRepo{CreateUserFunc: func(user *models.User) error {
		created = user
		return nil
	}}

	user, err := newUserService(userRepo, authRepo).InviteUser(
		"  PERSON@Example.COM ", "  Ana  ", "  Pérez  ",
	)

	require.NoError(t, err)
	assert.Equal(t, "person@example.com", invitedEmail)
	assert.Equal(t, "Ana", invitedFirstName)
	assert.Equal(t, "Pérez", invitedLastName)
	require.NotNil(t, created)
	assert.Equal(t, "cognito-sub-immutable", created.CognitoSub)
	assert.Equal(t, created, user)
}

func TestInviteUserForTenant_PreservesBrandingContext(t *testing.T) {
	var invitedVia string
	authRepo := &tenantAwareMockAuthRepo{
		mockAuthRepo: &mockAuthRepo{},
		InviteUserForTenantFunc: func(email, firstName, lastName, tenantCode, provider string) (*dtos.AuthUser, error) {
			invitedVia = tenantCode
			return &dtos.AuthUser{
				Sub: "tenant-invite-sub", Email: email,
				FirstName: firstName, LastName: lastName, IsActive: true,
			}, nil
		},
	}
	svc := NewUserService(&mockUserRepo{}, authRepo, nil)

	_, err := svc.InviteUserForTenant("person@example.com", "Ana", "Pérez", "cafettonhouse")

	require.NoError(t, err)
	assert.Equal(t, "cafettonhouse", invitedVia)
}

func TestInviteUser_RejectsInvalidIdentityBeforeCallingCognito(t *testing.T) {
	called := false
	authRepo := &mockAuthRepo{InviteUserFunc: func(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
		called = true
		return nil, nil
	}}

	_, err := newUserService(nil, authRepo).InviteUser("not-an-email", "A", "B")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInviteIdentity)
	assert.False(t, called)
}

func TestInviteUser_RollsBackCognitoWhenLocalPersistenceFails(t *testing.T) {
	deletedSub := ""
	authRepo := &mockAuthRepo{
		InviteUserFunc: func(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
			return &dtos.AuthUser{
				Sub: "rollback-sub", Email: email,
				FirstName: firstName, LastName: lastName,
			}, nil
		},
		DeleteUserFunc: func(sub, provider string) error {
			deletedSub = sub
			return nil
		},
	}
	userRepo := &mockUserRepo{CreateUserFunc: func(user *models.User) error {
		return errors.New("database unavailable")
	}}

	_, err := newUserService(userRepo, authRepo).InviteUser("person@example.com", "Ana", "Pérez")

	require.EqualError(t, err, "database unavailable")
	assert.Equal(t, "rollback-sub", deletedSub)
}
