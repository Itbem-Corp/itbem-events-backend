package configuration

import (
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func newCORSTestServer(cfg *models.Config) *echo.Echo {
	e := echo.New()
	e.Use(GetCORSConfig(cfg))
	e.GET("/ping", func(c echo.Context) error {
		return c.NoContent(http.StatusOK)
	})
	return e
}

func performCORSRequest(e *echo.Echo, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(echo.HeaderOrigin, origin)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func performCORSPreflight(e *echo.Echo, origin, method string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, "/ping", nil)
	req.Header.Set(echo.HeaderOrigin, origin)
	req.Header.Set(echo.HeaderAccessControlRequestMethod, method)
	if len(headers) > 0 {
		req.Header.Set(echo.HeaderAccessControlRequestHeaders, strings.Join(headers, ", "))
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestGetCORSConfigAllowsProductionOrigins(t *testing.T) {
	e := newCORSTestServer(&models.Config{})

	for _, origin := range []string{
		"https://eventiapp.com.mx",
		"https://www.eventiapp.com.mx",
		"https://dashboard.eventiapp.com.mx",
		"https://dashboard.itbem.com",
		"https://dashboard.itbem.com.mx",
		"https://dashboard.cafettonhouse.com",
	} {
		rec := performCORSRequest(e, origin)

		if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != origin {
			t.Fatalf("expected origin %q to be allowed, got %q", origin, got)
		}
	}
}

func TestGetCORSConfigAllowsDashboardPatchPreflight(t *testing.T) {
	e := newCORSTestServer(&models.Config{})

	rec := performCORSPreflight(e, "https://dashboard.eventiapp.com.mx", http.MethodPatch)

	if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "https://dashboard.eventiapp.com.mx" {
		t.Fatalf("expected dashboard origin to be allowed, got %q", got)
	}
	if got := rec.Header().Get(echo.HeaderAccessControlAllowMethods); !strings.Contains(got, http.MethodPatch) {
		t.Fatalf("expected PATCH to be allowed in preflight methods, got %q", got)
	}
}

func TestGetCORSConfigAllowsFrontendRequestHeaders(t *testing.T) {
	e := newCORSTestServer(&models.Config{
		CorsAllowOrigins: "http://localhost:3000,http://localhost:4321",
	})

	tests := []struct {
		name   string
		origin string
		method string
	}{
		{
			name:   "dashboard authorization json",
			origin: "http://localhost:3000",
			method: http.MethodPut,
		},
		{
			name:   "public frontend json upload metadata",
			origin: "http://localhost:4321",
			method: http.MethodPost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := performCORSPreflight(e, tt.origin, tt.method, echo.HeaderContentType, echo.HeaderAuthorization, "Idempotency-Key", utils.HeaderEventAccessToken, "X-Application-Code", "X-Workspace-Mode", "X-Organization-ID", "X-Organization-Context")

			if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != tt.origin {
				t.Fatalf("expected origin %q to be allowed, got %q", tt.origin, got)
			}
			allowedHeaders := rec.Header().Get(echo.HeaderAccessControlAllowHeaders)
			for _, header := range []string{echo.HeaderContentType, echo.HeaderAuthorization, "Idempotency-Key", utils.HeaderEventAccessToken, "X-Application-Code", "X-Workspace-Mode", "X-Organization-ID", "X-Organization-Context"} {
				if !strings.Contains(strings.ToLower(allowedHeaders), strings.ToLower(header)) {
					t.Fatalf("expected %s to be allowed in preflight headers, got %q", header, allowedHeaders)
				}
			}
		})
	}
}

func TestGetCORSConfigAllowsTrimmedExtraOrigins(t *testing.T) {
	e := newCORSTestServer(&models.Config{
		CorsAllowOrigins: " http://localhost:3000, http://localhost:4321 ",
	})

	for _, origin := range []string{
		"http://localhost:3000",
		"http://localhost:4321",
	} {
		rec := performCORSRequest(e, origin)

		if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != origin {
			t.Fatalf("expected origin %q to be allowed, got %q", origin, got)
		}
	}
}

func TestGetCORSConfigNormalizesExtraOriginsWithSlashesAndSubpaths(t *testing.T) {
	e := newCORSTestServer(&models.Config{
		CorsAllowOrigins: " https://preview.example.com/eventi-public/, http://localhost:4321/ ",
	})

	for _, origin := range []string{
		"https://preview.example.com",
		"http://localhost:4321",
	} {
		rec := performCORSRequest(e, origin)

		if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != origin {
			t.Fatalf("expected origin %q to be allowed, got %q", origin, got)
		}
	}
}

func TestGetCORSConfigRejectsUnknownOrigins(t *testing.T) {
	e := newCORSTestServer(&models.Config{})
	rec := performCORSRequest(e, "https://malicious.example.com")

	if got := rec.Header().Get(echo.HeaderAccessControlAllowOrigin); got != "" {
		t.Fatalf("expected unknown origin to be rejected, got %q", got)
	}
}
