package token

import (
	"events-stocks/models"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireTenantHostLimitsPublicEventiAppSurface(t *testing.T) {
	e := echo.New()
	cfg := &models.Config{TenantHostMap: "api.eventiapp.com.mx=eventiapp,api.itbem.com.mx=itbem"}
	handler := RequireTenantHost(cfg, "eventiapp")(func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	for _, tc := range []struct {
		name string
		host string
		want int
	}{
		{name: "eventiapp host", host: "api.eventiapp.com.mx", want: http.StatusNoContent},
		{name: "eventiapp host with port", host: "api.eventiapp.com.mx:443", want: http.StatusNoContent},
		{name: "itbem host", host: "api.itbem.com.mx", want: http.StatusForbidden},
		{name: "unknown host", host: "unknown.example.com", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/events/page-spec", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			require.NoError(t, handler(e.NewContext(req, rec)))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}
