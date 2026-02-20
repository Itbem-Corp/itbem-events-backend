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
	CreateUserFunc        func(user *models.User) error
	UpdateUserFunc        func(user *models.User) error
	DeleteUserFunc        func(id uuid.UUID) error
	GetUserByIDFunc       func(id uuid.UUID) (*models.User, error)
	GetUserByCognitoSubFunc func(sub string) (*models.User, error)
	UpdateUserFieldsFunc  func(userID uuid.UUID, fields map[string]interface{}) error
	ClearProfileImageFunc func(userID uuid.UUID) error
	SetUserRootFunc       func(userID uuid.UUID, isRoot bool) error
	ListAllUsersFunc          func() ([]models.User, error)
	ListAllUsersPaginatedFunc func(page, pageSize int) ([]models.User, int64, error)
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
func (m *mockUserRepo) ListAllUsersPaginated(page, pageSize int) ([]models.User, int64, error) {
	if m.ListAllUsersPaginatedFunc != nil {
		return m.ListAllUsersPaginatedFunc(page, pageSize)
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
