package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

func TestRedactedRequestURIUsesRouteTemplateAndNeverLogsQueryValues(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/invitations/ByToken/RAW-SECRET?pretty_token=PRETTY-SECRET&page=2&search=ana%40example.com",
		nil,
	)
	c := e.NewContext(req, httptest.NewRecorder())
	c.SetPath("/api/invitations/ByToken/:token")
	c.SetParamNames("token")
	c.SetParamValues("RAW-SECRET")

	got := redactedRequestURI(c)

	assert.Equal(t,
		"/api/invitations/ByToken/:token?page=<redacted>&pretty_token=<redacted>&search=<redacted>",
		got,
	)
	assert.NotContains(t, got, "RAW-SECRET")
	assert.NotContains(t, got, "PRETTY-SECRET")
	assert.NotContains(t, got, "ana@example.com")
}

func TestRedactedRequestURINeverLogsUnknownPathValues(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/missing/RAW-PATH-SECRET?preview_token=secret", nil)
	c := e.NewContext(req, httptest.NewRecorder())

	got := redactedRequestURI(c)
	assert.Equal(t, "/<unmatched>?preview_token=<redacted>", got)
	assert.NotContains(t, got, "RAW-PATH-SECRET")
	assert.NotContains(t, got, "secret")
}
