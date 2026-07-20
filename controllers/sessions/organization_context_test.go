package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/organizationcontext"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestIssueOrganizationContextReturnsSessionBoundToken(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	organizationID := uuid.Must(uuid.NewV4())
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/session/organization-context", strings.NewReader(`{"organization_id":"`+organizationID.String()+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := e.NewContext(req, recorder)
	context.Set("cognito_sub", "subject-1")
	context.Set(applicationaccess.ContextKey, &applications.Session{
		Application:   models.Application{Code: "eventiapp"},
		Organizations: []applications.OrganizationAccess{{ID: organizationID}},
	})

	require.NoError(t, IssueOrganizationContext(context))
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, organizationcontext.Validate(response.Data.Token, "subject-1", "eventiapp", organizationID))
}

func TestIssueOrganizationContextRejectsOrganizationOutsideSession(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", "organization-context-test-secret-at-least-32-bytes")
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/session/organization-context", strings.NewReader(`{"organization_id":"`+uuid.Must(uuid.NewV4()).String()+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := e.NewContext(req, recorder)
	context.Set(applicationaccess.ContextKey, &applications.Session{Application: models.Application{Code: "eventiapp"}})

	require.NoError(t, IssueOrganizationContext(context))
	require.Equal(t, http.StatusForbidden, recorder.Code)
}
