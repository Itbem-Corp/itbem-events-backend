package configuration

import (
	"events-stocks/models"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"net/http"
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
	}
	if cfg.CorsAllowOrigins != "" {
		for _, o := range strings.Split(cfg.CorsAllowOrigins, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				origins = append(origins, trimmed)
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
		},
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodDelete,
			http.MethodOptions,
		},
	})
}
