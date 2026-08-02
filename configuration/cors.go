package configuration

import (
	requestcontract "events-stocks/internal/requestcontext"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"net/http"
	"net/url"
	"strings"
)

// GetCORSConfig builds the CORS middleware. Production origins are hardcoded.
// Extra origins (e.g. localhost for local dev) are read from CORS_ALLOW_ORIGINS
// as a comma-separated list and appended at runtime.
func GetCORSConfig(cfg *models.Config) echo.MiddlewareFunc {
	origins := []string{
		"https://eventiapp.com.mx",
		"https://www.eventiapp.com.mx",
		"https://api.eventiapp.com.mx",
		"https://dashboard.eventiapp.com.mx",
		"https://dashboard.itbem.com",
		"https://dashboard.itbem.com.mx",
		"https://dashboard.cafettonhouse.com",
	}
	if cfg.CorsAllowOrigins != "" {
		for _, o := range strings.Split(cfg.CorsAllowOrigins, ",") {
			if origin := normalizeCORSOrigin(o); origin != "" {
				origins = append(origins, origin)
			}
		}
	}

	return middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: origins,
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
			"Idempotency-Key",
			utils.HeaderEventAccessToken,
			requestcontract.HeaderApplicationCode,
			requestcontract.HeaderWorkspaceMode,
			requestcontract.HeaderOrganizationID,
			requestcontract.HeaderOrganizationContext,
		},
		ExposeHeaders: []string{"Idempotency-Replayed", "Idempotency-Status"},
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodOptions,
		},
	})
}

func normalizeCORSOrigin(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if trimmed == "*" {
		return trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(trimmed, "/")
	}
	return parsed.Scheme + "://" + parsed.Host
}
