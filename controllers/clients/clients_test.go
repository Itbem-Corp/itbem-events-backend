package clients

import (
	"bytes"
	"context"
	"errors"
	"events-stocks/dtos"
	customValidator "events-stocks/middleware/validator"
	"events-stocks/models"
	clientService "events-stocks/services/clients"
	"events-stocks/services/ports"
	resourceService "events-stocks/services/resources"
	usersService "events-stocks/services/users"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEchoCtx(method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	e.Validator = customValidator.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// ── Mocks ─────────────────────────────────────────────────────────────────────

type mockClientRepo struct{}

func (m *mockClientRepo) CreateClient(client *models.Client) error { return nil }
func (m *mockClientRepo) GetClientByID(id uuid.UUID) (*models.Client, error) {
	return &models.Client{}, nil
}
func (m *mockClientRepo) UpdateClient(client *models.Client) error { return nil }
func (m *mockClientRepo) DeleteClient(id uuid.UUID) error          { return nil }
func (m *mockClientRepo) GetAllClients() ([]models.Client, error)  { return nil, nil }
func (m *mockClientRepo) ListClientsPaginated(_ *uuid.UUID, _ dtos.ClientsListQuery) ([]models.Client, int64, error) {
	return nil, 0, nil
}
func (m *mockClientRepo) GetClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	return nil, nil
}
func (m *mockClientRepo) GetChildrenClients(parentID uuid.UUID) ([]models.Client, error) {
	return nil, nil
}
func (m *mockClientRepo) CheckAccessRecursive(userID, targetClientID uuid.UUID) (bool, string) {
	return true, "OWNER"
}
func (m *mockClientRepo) IsMember(userID, clientID uuid.UUID) (bool, string)           { return true, "OWNER" }
func (m *mockClientRepo) AddMember(member *models.ClientMember) error                  { return nil }
func (m *mockClientRepo) RemoveMember(clientID, userID uuid.UUID) error                { return nil }
func (m *mockClientRepo) UpdateMemberRole(clientID, userID, newRoleID uuid.UUID) error { return nil }
func (m *mockClientRepo) GetMemberRole(clientID, userID uuid.UUID) (*models.ClientRole, error) {
	return &models.ClientRole{}, nil
}
func (m *mockClientRepo) GetMembers(clientID uuid.UUID) ([]models.ClientMember, error) {
	return nil, nil
}
func (m *mockClientRepo) DeleteAllMembers(clientID uuid.UUID) error { return nil }
func (m *mockClientRepo) ListClientsByUser(userID uuid.UUID) ([]models.Client, error) {
	return nil, nil
}
func (m *mockClientRepo) CountClientsByUsers(userIDs []uuid.UUID) (map[uuid.UUID]int64, error) {
	return map[uuid.UUID]int64{}, nil
}

var _ ports.ClientRepository = (*mockClientRepo)(nil)

type mockClientRoleRepo struct{}

func (m *mockClientRoleRepo) GetByCode(code string) (*models.ClientRole, error) {
	return &models.ClientRole{}, nil
}
func (m *mockClientRoleRepo) GetByID(id uuid.UUID) (*models.ClientRole, error) {
	return &models.ClientRole{}, nil
}
func (m *mockClientRoleRepo) GetAssignableRoles(myHierarchyLevel int) ([]models.ClientRole, error) {
	return nil, nil
}

var _ ports.ClientRoleRepository = (*mockClientRoleRepo)(nil)

type mockClientTypeRepo struct{}

func (m *mockClientTypeRepo) GetByID(id uuid.UUID) (*models.ClientType, error) {
	return &models.ClientType{}, nil
}
func (m *mockClientTypeRepo) GetByCode(code string) (*models.ClientType, error) {
	return &models.ClientType{}, nil
}
func (m *mockClientTypeRepo) GetChildTypes(parentLevel int) ([]models.ClientType, error) {
	return nil, nil
}
func (m *mockClientTypeRepo) GetRootType() ([]models.ClientType, error) { return nil, nil }

var _ ports.ClientTypeRepository = (*mockClientTypeRepo)(nil)

type mockCacheRepo struct{}

func (m *mockCacheRepo) Invalidate(_, _ string) error                                  { return nil }
func (m *mockCacheRepo) DeleteKeysByPattern(_ context.Context, _ string) error         { return nil }
func (m *mockCacheRepo) GetKey(_ context.Context, _ string) (string, error)            { return "", nil }
func (m *mockCacheRepo) SaveKey(_ context.Context, _, _ string, _ time.Duration) error { return nil }

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

type mockControllerUserRepo struct {
	user *models.User
}

func (m *mockControllerUserRepo) CreateUser(user *models.User) error { return nil }
func (m *mockControllerUserRepo) UpdateUser(user *models.User) error { return nil }
func (m *mockControllerUserRepo) DeleteUser(id uuid.UUID) error      { return nil }
func (m *mockControllerUserRepo) GetUserByID(id uuid.UUID) (*models.User, error) {
	return nil, errors.New("record not found")
}
func (m *mockControllerUserRepo) GetUserByCognitoSub(sub string) (*models.User, error) {
	return m.user, nil
}
func (m *mockControllerUserRepo) GetUserByEmail(email string) (*models.User, error) {
	return nil, errors.New("record not found")
}
func (m *mockControllerUserRepo) UpdateUserFields(userID uuid.UUID, fields map[string]interface{}) error {
	return nil
}
func (m *mockControllerUserRepo) ClearProfileImage(userID uuid.UUID) error { return nil }
func (m *mockControllerUserRepo) SetUserRoot(userID uuid.UUID, isRoot bool) error {
	return nil
}
func (m *mockControllerUserRepo) ListAllUsers() ([]models.User, error) { return nil, nil }
func (m *mockControllerUserRepo) ListAllUsersPaginated(query dtos.AdminUsersListQuery) ([]models.User, int64, error) {
	return nil, 0, nil
}
func (m *mockControllerUserRepo) SetUserActive(userID uuid.UUID, active bool) error {
	return nil
}

var _ ports.UserRepository = (*mockControllerUserRepo)(nil)

type mockControllerAuthRepo struct{}

func (m *mockControllerAuthRepo) GetUser(sub string, provider string) (*dtos.AuthUser, error) {
	return &dtos.AuthUser{
		Sub:       sub,
		Email:     "member-admin@example.com",
		FirstName: "Member",
		LastName:  "Admin",
		IsActive:  true,
	}, nil
}
func (m *mockControllerAuthRepo) UpdateUser(sub string, attrs map[string]string, provider string) error {
	return nil
}
func (m *mockControllerAuthRepo) DeleteUser(sub string, provider string) error {
	return nil
}
func (m *mockControllerAuthRepo) CreateUser(req dtos.CreateAuthUserRequest, provider string) (*dtos.AuthUser, error) {
	return &dtos.AuthUser{
		Sub:       "created-sub",
		Email:     req.Email,
		FirstName: req.FirstName,
		LastName:  req.LastName,
		IsActive:  true,
	}, nil
}
func (m *mockControllerAuthRepo) SetUserEnabled(sub string, enabled bool, provider string) error {
	return nil
}
func (m *mockControllerAuthRepo) InviteUser(email, firstName, lastName, provider string) (*dtos.AuthUser, error) {
	return &dtos.AuthUser{
		Sub:       "invited-sub",
		Email:     email,
		FirstName: firstName,
		LastName:  lastName,
		IsActive:  true,
	}, nil
}

var _ ports.AuthProviderRepository = (*mockControllerAuthRepo)(nil)

func setAuthenticatedClientUser(t *testing.T, userID uuid.UUID) {
	t.Helper()

	user := &models.User{
		ID:         userID,
		CognitoSub: "client-controller-test-sub",
		Email:      "member-admin@example.com",
		FirstName:  "Member",
		LastName:   "Admin",
		IsActive:   true,
	}
	usersService.SetDefaultUserService(usersService.NewUserService(
		&mockControllerUserRepo{user: user},
		&mockControllerAuthRepo{},
		nil,
	))
	t.Cleanup(func() { usersService.SetDefaultUserService(nil) })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

type mockClientLogoObjectStorage struct{}

func (m *mockClientLogoObjectStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return false, "", nil
}
func (m *mockClientLogoObjectStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	return "https://signed.example.com/" + folder + "/" + filename, nil
}
func (m *mockClientLogoObjectStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return "", nil
}
func (m *mockClientLogoObjectStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "", nil
}
func (m *mockClientLogoObjectStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}
func (m *mockClientLogoObjectStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return nil
}
func (m *mockClientLogoObjectStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}
func (m *mockClientLogoObjectStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return "", nil
}
func (m *mockClientLogoObjectStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	return nil
}
func (m *mockClientLogoObjectStorage) DeleteFile(filename, folder, bucket, provider string) error {
	return nil
}
func (m *mockClientLogoObjectStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

var _ ports.ObjectStorageRepository = (*mockClientLogoObjectStorage)(nil)

func newMultipartLogoCtx(t *testing.T, method, path, filename, contentType string, content []byte) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()

	e := echo.New()
	e.Validator = customValidator.New()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, "logo", filename))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(method, path, body)
	req.Header.Set(echo.HeaderContentType, writer.FormDataContentType())
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestRemoveMember_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/clients/members/some-id", "")
	c.SetParamNames("user_id")
	c.SetParamValues("some-id")
	require.NoError(t, RemoveMember(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateMemberRole_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodPut, "/clients/members/some-id", `{"new_role_id":""}`)
	c.SetParamNames("user_id")
	c.SetParamValues("some-id")
	require.NoError(t, UpdateMemberRole(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRemoveMember_InvalidUserUUID_Returns400(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodDelete, "/clients/members/bad-uuid?client_id="+clientID.String(), "")
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("user_id")
	c.SetParamValues("bad-uuid")
	require.NoError(t, RemoveMember(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateMemberRole_InvalidClientUUID_Returns400(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	targetUserID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/clients/members/"+targetUserID.String()+"?client_id=bad-client", `{"new_role_id":""}`)
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("user_id")
	c.SetParamValues(targetUserID.String())
	require.NoError(t, UpdateMemberRole(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUpdateMemberRole_MissingNewRoleID_Returns400(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	targetUserID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodPut, "/clients/members/"+targetUserID.String()+"?client_id="+clientID.String(), `{}`)
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("user_id")
	c.SetParamValues(targetUserID.String())
	require.NoError(t, UpdateMemberRole(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInviteUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	// InviteUser now requires cognito_sub auth before processing the body
	c, rec := newEchoCtx(http.MethodPost, "/clients/invite", `{"client_id":"","user_id":"","role_id":""}`)
	require.NoError(t, InviteUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestDeleteClient_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/clients/some-id", "")
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, DeleteClient(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestListMyClients_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/clients", "")
	require.NoError(t, ListMyClients(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGetClient_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/clients/some-id", "")
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, GetClient(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCreateNewClient_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/clients", `{"name":"Test","client_type_id":"","parent_id":null}`)
	require.NoError(t, CreateNewClient(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateClient_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodPut, "/clients/some-id", `{"name":"New Name"}`)
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, UpdateClient(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateClient_InvalidLogoUpload_Returns400(t *testing.T) {
	orig := clientSvc
	resourceSvc := resourceService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourceService.ResourceServiceDeps{Storage: &mockClientLogoObjectStorage{}},
	)
	clientSvc = clientService.NewClientService(
		&mockClientRepo{},
		&mockClientRoleRepo{},
		&mockClientTypeRepo{},
		resourceSvc,
		nil,
		nil,
	)
	t.Cleanup(func() { clientSvc = orig })
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	clientID := uuid.Must(uuid.NewV4())
	c, rec := newMultipartLogoCtx(t, http.MethodPut, "/clients/"+clientID.String(), "intro.mp4", "video/mp4", []byte("not-an-image"))
	c.Set("cognito_sub", "client-controller-test-sub")
	c.SetParamNames("id")
	c.SetParamValues(clientID.String())

	require.NoError(t, UpdateClient(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid logo")
	assert.Contains(t, rec.Body.String(), "unsupported image type")
}

func TestGetMySubClients_MissingCognitoSub_Returns401(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/clients/children", "")
	require.NoError(t, GetMySubClients(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGetMySubClients_MissingParentID_Returns400(t *testing.T) {
	orig := clientSvc
	clientSvc = nil
	defer func() { clientSvc = orig }()
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	c, rec := newEchoCtx(http.MethodGet, "/clients/children", "")
	c.Set("cognito_sub", "client-controller-test-sub")

	require.NoError(t, GetMySubClients(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Missing parent_id")
}

func TestGetMySubClients_WithParentID_Returns200(t *testing.T) {
	orig := clientSvc
	clientSvc = clientService.NewClientService(
		&mockClientRepo{},
		&mockClientRoleRepo{},
		&mockClientTypeRepo{},
		nil,
		nil,
		nil,
	)
	t.Cleanup(func() { clientSvc = orig })
	setAuthenticatedClientUser(t, uuid.Must(uuid.NewV4()))

	parentID := uuid.Must(uuid.NewV4())
	c, rec := newEchoCtx(http.MethodGet, "/clients/children?parent_id="+parentID.String(), "")
	c.Set("cognito_sub", "client-controller-test-sub")

	require.NoError(t, GetMySubClients(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Sub-clients list")
}
