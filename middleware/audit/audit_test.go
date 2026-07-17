package audit

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestShouldAuditOnlyStateChangingMethods(t *testing.T) {
	assert.False(t, shouldAudit(http.MethodGet, http.StatusOK))
	assert.False(t, shouldAudit(http.MethodHead, http.StatusOK))
	assert.False(t, shouldAudit(http.MethodOptions, http.StatusNoContent))
	assert.True(t, shouldAudit(http.MethodPost, http.StatusCreated))
	assert.True(t, shouldAudit(http.MethodPut, http.StatusOK))
	assert.True(t, shouldAudit(http.MethodPatch, http.StatusOK))
	assert.True(t, shouldAudit(http.MethodDelete, http.StatusNoContent))
	assert.True(t, shouldAudit(http.MethodGet, http.StatusForbidden))
	assert.True(t, shouldAudit(http.MethodGet, http.StatusUnauthorized))
}

func TestNewEntryUsesRouteTemplateAndNeverRawPathValues(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodDelete, "/api/users/secret-user-id", nil)
	request.Header.Set("User-Agent", "audit-test")
	recorder := httptest.NewRecorder()
	context := e.NewContext(request, recorder)
	context.SetPath("/api/users/:id")
	context.SetParamNames("id")
	context.SetParamValues("secret-user-id")
	context.Set("cognito_sub", "cognito-user")
	context.Set("tenant_code", "itbem")
	recorder.Header().Set(echo.HeaderXRequestID, "request-1")

	entry := newEntry(context, http.StatusNoContent)

	assert.Equal(t, "/api/users/:id", entry.Route)
	assert.NotContains(t, entry.Route, "secret-user-id")
	assert.Equal(t, "users", entry.ResourceType)
	assert.Equal(t, "secret-user-id", entry.ResourceID)
	assert.Equal(t, "cognito-user", entry.ActorCognitoSub)
	assert.Equal(t, "itbem", entry.TenantCode)
	assert.Equal(t, "request-1", entry.RequestID)
	assert.True(t, entry.Succeeded)
}

func TestResponseStatusPreservesDeniedAttempts(t *testing.T) {
	e := echo.New()
	context := e.NewContext(
		httptest.NewRequest(http.MethodPut, "/api/users/id/root-level", nil),
		httptest.NewRecorder(),
	)

	assert.Equal(t, http.StatusForbidden, responseStatus(context, echo.NewHTTPError(http.StatusForbidden)))
	assert.Equal(t, http.StatusInternalServerError, responseStatus(context, errors.New("boom")))
}
