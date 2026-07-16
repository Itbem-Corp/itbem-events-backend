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
	assert.False(t, shouldAudit(http.MethodGet))
	assert.False(t, shouldAudit(http.MethodHead))
	assert.False(t, shouldAudit(http.MethodOptions))
	assert.True(t, shouldAudit(http.MethodPost))
	assert.True(t, shouldAudit(http.MethodPut))
	assert.True(t, shouldAudit(http.MethodPatch))
	assert.True(t, shouldAudit(http.MethodDelete))
}

func TestNewEntryUsesRouteTemplateAndNeverRawPathValues(t *testing.T) {
	e := echo.New()
	request := httptest.NewRequest(http.MethodDelete, "/api/users/secret-user-id", nil)
	request.Header.Set("User-Agent", "audit-test")
	recorder := httptest.NewRecorder()
	context := e.NewContext(request, recorder)
	context.SetPath("/api/users/:id")
	context.Set("cognito_sub", "cognito-user")
	context.Set("tenant_code", "itbem")
	recorder.Header().Set(echo.HeaderXRequestID, "request-1")

	entry := newEntry(context, http.StatusNoContent)

	assert.Equal(t, "/api/users/:id", entry.Route)
	assert.NotContains(t, entry.Route, "secret-user-id")
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
