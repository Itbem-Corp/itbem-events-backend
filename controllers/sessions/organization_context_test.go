package sessions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"events-stocks/internal/organizationcontext"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/middleware/audit"
	"events-stocks/models"
	"events-stocks/services/applications"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
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

const organizationContextTestSecret = "organization-context-test-secret-at-least-32-bytes"

// organizationContextRequest builds the request the dashboard renewal sends:
// the body organization plus, optionally, the organization the middleware
// already resolved from X-Organization-ID.
func organizationContextRequest(session *applications.Session, body uuid.UUID, header *uuid.UUID) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/session/organization-context", strings.NewReader(`{"organization_id":"`+body.String()+`"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	context := e.NewContext(req, recorder)
	context.SetPath("/api/session/organization-context")
	context.Set("cognito_sub", "subject-1")
	context.Set(applicationaccess.ContextKey, session)
	if header != nil {
		context.Set(applicationaccess.ContextOrganizationID, *header)
	}
	return context, recorder
}

func memberSession(organizations ...uuid.UUID) *applications.Session {
	access := make([]applications.OrganizationAccess, 0, len(organizations))
	for _, organizationID := range organizations {
		access = append(access, applications.OrganizationAccess{ID: organizationID})
	}
	return &applications.Session{Application: models.Application{Code: "eventiapp"}, Organizations: access}
}

func TestIssueOrganizationContextMarksOnlySameOrganizationRenewals(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", organizationContextTestSecret)
	first := uuid.Must(uuid.NewV4())
	second := uuid.Must(uuid.NewV4())
	outside := uuid.Must(uuid.NewV4())

	t.Run("header equals body", func(t *testing.T) {
		context, recorder := organizationContextRequest(memberSession(first, second), first, &first)
		require.NoError(t, IssueOrganizationContext(context))
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.True(t, audit.SkipOnSuccessRequested(context))
	})
	t.Run("switch between allowed organizations", func(t *testing.T) {
		context, recorder := organizationContextRequest(memberSession(first, second), second, &first)
		require.NoError(t, IssueOrganizationContext(context))
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.False(t, audit.SkipOnSuccessRequested(context))
	})
	t.Run("no header organization", func(t *testing.T) {
		context, recorder := organizationContextRequest(memberSession(first), first, nil)
		require.NoError(t, IssueOrganizationContext(context))
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.False(t, audit.SkipOnSuccessRequested(context))
	})
	t.Run("body organization outside the session", func(t *testing.T) {
		context, recorder := organizationContextRequest(memberSession(first), outside, &outside)
		require.NoError(t, IssueOrganizationContext(context))
		assert.Equal(t, http.StatusForbidden, recorder.Code)
		assert.False(t, audit.SkipOnSuccessRequested(context))
	})
	t.Run("credential cannot be issued", func(t *testing.T) {
		t.Setenv("ORGANIZATION_CONTEXT_SECRET", "")
		context, recorder := organizationContextRequest(memberSession(first), first, &first)
		require.NoError(t, IssueOrganizationContext(context))
		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		assert.False(t, audit.SkipOnSuccessRequested(context))
	})
}

func TestIssueOrganizationContextRootRenewalStillValidatesBody(t *testing.T) {
	t.Setenv("ORGANIZATION_CONTEXT_SECRET", organizationContextTestSecret)
	listed := uuid.Must(uuid.NewV4())
	unlisted := uuid.Must(uuid.NewV4())
	root := &applications.Session{
		Application:   models.Application{Code: "eventiapp", AllowsPlatformAdmin: true},
		Organizations: []applications.OrganizationAccess{{ID: listed, AccessRole: "PLATFORM"}},
	}
	root.User.IsRoot = true

	context, recorder := organizationContextRequest(root, listed, &listed)
	require.NoError(t, IssueOrganizationContext(context))
	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.True(t, audit.SkipOnSuccessRequested(context))

	// A root's header is not checked against the session, but the body is.
	context, recorder = organizationContextRequest(root, unlisted, &unlisted)
	require.NoError(t, IssueOrganizationContext(context))
	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.False(t, audit.SkipOnSuccessRequested(context))
}
