package users

import (
	customValidator "events-stocks/middleware/validator"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestGetUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/users/me", "")
	// cognito_sub not set in context → handler returns 401 before any service call
	require.NoError(t, GetUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUpdateUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodPut, "/users/me", `{"first_name":"Ana","last_name":"Garcia"}`)
	// cognito_sub not set in context → handler returns 401 before any service call
	require.NoError(t, UpdateUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestDeleteUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodDelete, "/users/me", "")
	require.NoError(t, DeleteUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestListAllUsers_MissingCognitoSub_Returns401(t *testing.T) {
	orig := adminSvc
	adminSvc = nil
	defer func() { adminSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/admin/users", "")
	require.NoError(t, ListAllUsers(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestDeactivateUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodPatch, "/admin/users/some-id/deactivate", "")
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, DeactivateUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestActivateUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodPatch, "/admin/users/some-id/activate", "")
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, ActivateUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGetUserDetail_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/admin/users/some-id", "")
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, GetUserDetail(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestListUserClients_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodGet, "/admin/users/some-id/clients", "")
	c.SetParamNames("id")
	c.SetParamValues("some-id")
	require.NoError(t, ListUserClients(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestInviteUser_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/admin/users/invite", `{"email":"test@test.com"}`)
	require.NoError(t, InviteUser(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestUploadAvatar_MissingCognitoSub_Returns401(t *testing.T) {
	orig := userSvc
	userSvc = nil
	defer func() { userSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/users/me/avatar", "")
	require.NoError(t, UploadAvatar(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
