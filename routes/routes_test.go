package routes

import (
	"encoding/json"
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuestSummaryContractResolvesThroughProtectedGuestKeyRoute(t *testing.T) {
	e := echo.New()
	ConfigurarRutas(e, &models.Config{})
	eventID := uuid.Must(uuid.NewV4())
	path := "/api/guests/summary:" + eventID.String()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	e.Router().Find(http.MethodGet, path, c)

	assert.Equal(t, "/api/guests/:key", c.Path())
	assert.Equal(t, "summary:"+eventID.String(), c.Param("key"))
}

func TestConfigurarRutasRegistersFrontendContractRoutes(t *testing.T) {
	e := echo.New()
	ConfigurarRutas(e, &models.Config{})

	registered := map[string]bool{}
	for _, route := range e.Routes() {
		registered[route.Method+" "+route.Path] = true
	}

	requiredRoutes := []string{
		// Cafetton public renderer / RSVP / moments contracts.
		"GET /api/events/page-spec",
		"GET /api/events/:identifier/page-spec",
		"GET /api/events/:identifier/meta",
		"POST /api/events/:identifier/view",
		"POST /api/events/:identifier/verify-access",
		"GET /api/events/phrases",
		"GET /api/events/section/:sectionId/attendees",
		"GET /api/resources/:id",
		"GET /api/resources/section/:key",
		"GET /api/invitations/ByToken",
		"GET /api/invitations/ByToken/:token",
		"POST /api/invitations/rsvp",
		"GET /api/events/:identifier/moments",
		"POST /api/events/:identifier/moments",
		"POST /api/events/:identifier/performance",
		"POST /api/events/:identifier/moments/upload-url",
		"POST /api/events/:identifier/moments/confirm",
		"POST /api/events/:identifier/moments/shared",
		"POST /api/events/:identifier/moments/shared/upload-url",
		"POST /api/events/:identifier/moments/shared/batch-upload-urls",
		"POST /api/events/:identifier/moments/shared/confirm",
		"POST /api/events/:identifier/moments/shared/multipart/start",
		"POST /api/events/:identifier/moments/shared/multipart/complete",
		"POST /api/events/:identifier/moments/shared/multipart/abort",

		// Dashboard protected contracts.
		"GET /api/session",
		"GET /api/audit-logs",
		"GET /api/events",
		"GET /api/events/dashboard",
		"GET /api/events/all",
		"POST /api/events",
		"GET /api/events/:id/detail",
		"PUT /api/events/:id",
		"DELETE /api/events/:id",
		"POST /api/events/:id/duplicate",
		"POST /api/events/:id/preview-token",
		"POST /api/events/:id/cover",
		"DELETE /api/events/:id/cover",
		"POST /api/events/:id/repair",
		"GET /api/events/:id/config",
		"PUT /api/events/:id/config",
		"GET /api/events/:id/analytics",
		"GET /api/events/:id/invitations",
		"POST /api/invitations/:id/resend",
		"GET /api/events/:id/sections",
		"POST /api/events/:id/sections",
		"PATCH /api/events/:id/sections/reorder",
		"PUT /api/sections/:id",
		"DELETE /api/sections/:id",
		"GET /api/events/:id/tables",
		"POST /api/events/:id/tables",
		"PUT /api/events/:id/tables/assign",
		"PUT /api/tables/:id",
		"DELETE /api/tables/:id",
		"POST /api/resources",
		"POST /api/resources/multiple",
		"GET /api/admin/resources/section/:key",
		"PUT /api/resources/:id/content",
		"PUT /api/resources/:id/replace",
		"DELETE /api/resources/:id",
		"GET /api/guests/:key",
		"POST /api/guests",
		"POST /api/guests/batch",
		"PUT /api/guests/:id",
		"DELETE /api/guests/bulk",
		"DELETE /api/guests/:id",
		"GET /api/moments",
		"GET /api/moments/summary",
		"GET /api/moments/in-flight",
		"GET /api/moments/reoptimizing",
		"PATCH /api/moments/reorder",
		"POST /api/moments/batch/reoptimize",
		"POST /api/moments/bulk-approve",
		"DELETE /api/moments/bulk",
		"GET /api/moments/:id",
		"POST /api/moments",
		"PUT /api/moments/:id/requeue",
		"PUT /api/moments/:id",
		"DELETE /api/moments/:id",
		"POST /api/clients",
		"GET /api/clients",
		"GET /api/clients/children",
		"GET /api/clients/:id",
		"PUT /api/clients/:id",
		"DELETE /api/clients/:id",
		"POST /api/clients/invite",
		"GET /api/clients/members",
		"POST /api/clients/members",
		"PUT /api/clients/members/:user_id",
		"DELETE /api/clients/members/:user_id",
		"GET /api/clients/:id/member-applications/:user_id",
		"PUT /api/clients/:id/member-applications/:user_id/:application_code",
		"GET /api/users",
		"PUT /api/users",
		"DELETE /api/users",
		"GET /api/users/all",
		"GET /api/users/:id",
		"PUT /api/users/:id",
		"DELETE /api/users/:id",
		"GET /api/users/:id/clients",
		"PUT /api/users/:id/activate",
		"PUT /api/users/:id/deactivate",
		"POST /api/users/invite",
		"GET /api/automation/portfolio",
		"POST /api/users/avatar",
		"DELETE /api/users/avatar",
		"GET /api/cache/flush/:key",
		"GET /api/cache/flush-all",
		"POST /api/fonts/upload",
		"GET /api/catalogs/client-types",
		"GET /api/catalogs/roles",
		"GET /api/catalogs/design-templates",
		"GET /api/catalogs/design-templates/:id",
		"GET /api/catalogs/color-palettes",
		"GET /api/catalogs/font-sets",
		"GET /api/catalogs/resource-types",
		"GET /api/catalogs/guest-statuses",
		"GET /api/event-types",
	}

	for _, route := range requiredRoutes {
		assert.True(t, registered[route], "expected route %s to be registered", route)
	}
}

func TestConfigurarRutasKeepsStaticDashboardRoutesAheadOfPublicParams(t *testing.T) {
	e := echo.New()
	ConfigurarRutas(e, &models.Config{})

	for _, tc := range []struct {
		method string
		target string
		path   string
	}{
		{method: http.MethodGet, target: "/api/events/all", path: "/api/events/all"},
		{method: http.MethodGet, target: "/api/events/phrases", path: "/api/events/phrases"},
		{method: http.MethodGet, target: "/api/events/page-spec", path: "/api/events/page-spec"},
		{method: http.MethodGet, target: "/api/cache/flush-all", path: "/api/cache/flush-all"},
		{method: http.MethodGet, target: "/api/invitations/ByToken", path: "/api/invitations/ByToken"},
	} {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		e.Router().Find(tc.method, tc.target, c)

		assert.Equal(t, tc.path, c.Path(), tc.target)
	}
}

func TestPublicRateLimiterDenyHandlerUsesAPIResponseEnvelope(t *testing.T) {
	e := echo.New()
	e.GET("/limited", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	}, publicRateLimiter())

	var rec *httptest.ResponseRecorder
	for i := 0; i < 41; i++ {
		req := httptest.NewRequest(http.MethodGet, "/limited", nil)
		req.Header.Set(echo.HeaderXRealIP, "203.0.113.10")
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
	}

	require.NotNil(t, rec)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	var body map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	assert.Equal(t, float64(http.StatusTooManyRequests), body["status"])
	assert.Equal(t, "Too many requests, please slow down", body["message"])
	assert.NotContains(t, body, "data")
	assert.NotContains(t, body, "error")
}

func TestGitHubReviewRateLimiterUsesSafeWebhookEnvelope(t *testing.T) {
	e := echo.New()
	e.POST("/github", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, githubReviewRateLimiter())
	for index := 0; index < 20; index++ {
		request := httptest.NewRequest(http.MethodPost, "/github", nil)
		request.RemoteAddr = "198.51.100.8:9000"
		response := httptest.NewRecorder()
		e.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("burst request %d = %d", index, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/github", nil)
	request.RemoteAddr = "198.51.100.8:9000"
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "Too many GitHub review events") {
		t.Fatalf("webhook rate limit response = %d %s", response.Code, response.Body.String())
	}
}

func TestPublicAccessCacheControlMarksProofHeaderRequestsNoStore(t *testing.T) {
	e := echo.New()
	e.GET("/api/events/:identifier/moments", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())

	req := httptest.NewRequest(http.MethodGet, "/api/events/demo/moments", nil)
	req.Header.Set(utils.HeaderEventAccessToken, "proof-token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl))
	assert.Equal(t, "no-cache", rec.Header().Get("Pragma"))
	assert.Equal(t, "0", rec.Header().Get("Expires"))
	assert.Contains(t, rec.Header().Get(echo.HeaderVary), utils.HeaderEventAccessToken)
}

func TestPublicAccessCacheControlMarksTokenScopedRequestsNoStore(t *testing.T) {
	e := echo.New()
	e.GET("/api/invitations/ByToken/:token", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/:identifier/page-spec", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/:identifier/meta", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())

	for _, path := range []string{
		"/api/invitations/ByToken/INVITE123",
		"/api/events/demo/page-spec?preview_token=PREVIEW123",
		"/api/events/demo/page-spec?previewToken=PREVIEW123",
		"/api/events/demo/page-spec?PreviewToken=PREVIEW123",
		"/api/events/demo/page-spec?token=INVITE123",
		"/api/events/demo/page-spec?Token=INVITE123",
		"/api/events/demo/page-spec?invitation_token=INVITE123",
		"/api/events/demo/page-spec?invitationToken=INVITE123",
		"/api/events/demo/page-spec?InvitationToken=INVITE123",
		"/api/events/demo/page-spec?pretty_token=PRETTY123",
		"/api/events/demo/page-spec?prettyToken=PRETTY123",
		"/api/events/demo/page-spec?PrettyToken=PRETTY123",
		"/api/events/demo/page-spec?event_access_token=PROOF123",
		"/api/events/demo/page-spec?eventAccessToken=PROOF123",
		"/api/events/demo/page-spec?EventAccessToken=PROOF123",
		"/api/events/demo/meta?preview_token=PREVIEW123",
		"/api/events/demo/meta?token=INVITE123",
		"/api/events/demo/meta?event_access_token=PROOF123",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl), path)
		assert.Contains(t, rec.Header().Get(echo.HeaderVary), utils.HeaderEventAccessToken, path)
	}
}

func TestPublicAccessCacheControlMarksAnonymousPublicAccessContentNoStore(t *testing.T) {
	e := echo.New()
	e.GET("/api/events/page-spec", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/:identifier/page-spec", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/:identifier/meta", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/:identifier/moments", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/resources/:id", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/resources/section/:key", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/section/:sectionId/attendees", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())

	for _, path := range []string{
		"/api/events/page-spec?token=ABC123",
		"/api/events/demo/page-spec",
		"/api/events/demo/moments",
		"/api/resources/resource-1",
		"/api/resources/section/section-1",
		"/api/events/section/section-1/attendees",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl), path)
		assert.Equal(t, "no-cache", rec.Header().Get("Pragma"), path)
		assert.Equal(t, "0", rec.Header().Get("Expires"), path)
	}
}

func TestPublicAccessCacheControlMarksPublicMutationsNoStore(t *testing.T) {
	e := echo.New()
	e.POST("/api/events/:identifier/verify-access", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"accessToken": "proof-token"})
	}, publicAccessCacheControl())
	e.POST("/api/invitations/rsvp", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())

	for _, path := range []string{
		"/api/events/demo/verify-access",
		"/api/invitations/rsvp",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "no-store", rec.Header().Get(echo.HeaderCacheControl), path)
		assert.Equal(t, "no-cache", rec.Header().Get("Pragma"), path)
		assert.Equal(t, "0", rec.Header().Get("Expires"), path)
		assert.Contains(t, rec.Header().Get(echo.HeaderVary), utils.HeaderEventAccessToken, path)
	}
}

func TestPublicAccessCacheControlLeavesAnonymousRequestsUnmarked(t *testing.T) {
	e := echo.New()
	e.GET("/api/events/phrases", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())
	e.GET("/api/events/:identifier/meta", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	}, publicAccessCacheControl())

	for _, path := range []string{
		"/api/events/phrases?type=BODA",
		"/api/events/demo/meta",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Empty(t, rec.Header().Get(echo.HeaderCacheControl), path)
		assert.Empty(t, rec.Header().Get(echo.HeaderVary), path)
	}
}
