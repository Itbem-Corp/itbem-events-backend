package applicationaccess

import (
	"errors"
	"events-stocks/services/applications"
	"events-stocks/utils"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

const ContextKey = "application_session"

var sessionService *applications.SessionService

func Configure(service *applications.SessionService) {
	sessionService = service
}

// Require rejects a valid Cognito identity when it is not entitled to the
// application selected by the token audience/API hostname.
func Require(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if sessionService == nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Application access unavailable", "session service is not configured")
		}
		sub, _ := c.Get("cognito_sub").(string)
		tenant, _ := c.Get("tenant_code").(string)
		session, err := sessionService.Resolve(sub, tenant)
		if err != nil {
			if errors.Is(err, applications.ErrApplicationAccessDenied) {
				return utils.Error(c, http.StatusForbidden, "Application access denied", "Your account is not enabled for this application")
			}
			return utils.Error(c, http.StatusServiceUnavailable, "Application access unavailable", err.Error())
		}
		c.Set(ContextKey, session)
		if required := requiredSurfaceCapability(c.Request().Method, c.Request().URL.Path); required != "" &&
			!sessionHasAnyCapability(session, strings.Split(required, "|")...) {
			return utils.Error(
				c,
				http.StatusForbidden,
				"Application capability denied",
				"This API surface is not enabled for the authenticated application",
			)
		}
		return next(c)
	}
}

func sessionHasAnyCapability(session *applications.Session, expected ...string) bool {
	if session == nil {
		return false
	}
	for _, actual := range session.Capabilities {
		for _, candidate := range expected {
			if actual == candidate {
				return true
			}
		}
	}
	return false
}

// requiredSurfaceCapability prevents a valid token for one product from
// calling another product's API surface. Resource-level authorization still
// runs inside controllers after this coarse application boundary.
func requiredSurfaceCapability(method, path string) string {
	path = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(path)), "/")
	method = strings.ToUpper(strings.TrimSpace(method))
	if path == "/api/session" || path == "/api/users" || strings.HasPrefix(path, "/api/users/avatar") {
		return ""
	}
	if strings.HasPrefix(path, "/api/users/") || path == "/api/users/all" {
		return "platform:users:view"
	}
	if strings.HasPrefix(path, "/api/catalogs/roles") {
		return "members:manage|applications:manage"
	}
	if strings.HasPrefix(path, "/api/catalogs/client-types") {
		return "organizations:manage"
	}
	if strings.HasPrefix(path, "/api/clients") {
		if strings.Contains(path, "/members") ||
			strings.Contains(path, "/member-applications") ||
			strings.HasSuffix(path, "/invite") {
			return "members:manage|applications:manage"
		}
		if method == http.MethodGet {
			return "organizations:view|members:manage|events:manage"
		}
		return "organizations:manage"
	}
	eventPrefixes := []string{
		"/api/events", "/api/guests", "/api/moments", "/api/invitations",
		"/api/sections", "/api/tables", "/api/resources", "/api/admin/resources",
		"/api/fonts", "/api/catalogs/design-templates", "/api/catalogs/color-palettes",
		"/api/catalogs/font-sets", "/api/catalogs/resource-types",
		"/api/catalogs/guest-statuses", "/api/event-types",
	}
	for _, prefix := range eventPrefixes {
		if strings.HasPrefix(path, prefix) {
			return "events:view"
		}
	}
	return ""
}
