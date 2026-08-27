package productmetricshttp

import (
	"net/http"
	"strings"
	"time"

	"events-stocks/middleware/applicationaccess"
	"events-stocks/services/applications"
	"events-stocks/services/productmetrics"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const organizationContextKey = "metrics_organization_id"

func ScopeOrganization(c echo.Context, clientID uuid.UUID) {
	if c != nil && clientID != uuid.Nil {
		c.Set(organizationContextKey, clientID)
	}
}

func Capture(next echo.HandlerFunc) echo.HandlerFunc { return middleware(next, "") }
func CaptureTenant(tenant string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc { return middleware(next, tenant) }
}
func middleware(next echo.HandlerFunc, fallbackTenant string) echo.HandlerFunc {
	return func(c echo.Context) error {
		started := time.Now()
		err := next(c)
		if collector := productmetrics.DefaultCollector(); collector != nil {
			tenant, _ := c.Get("tenant_code").(string)
			if strings.TrimSpace(tenant) == "" {
				tenant = fallbackTenant
			}
			userID := uuid.Nil
			if session, ok := c.Get(applicationaccess.ContextKey).(*applications.Session); ok && session != nil {
				userID = session.User.ID
			}
			collector.Record(productmetrics.RequestObservation{TenantCode: tenant, OrganizationID: organizationID(c), UserID: userID, Method: c.Request().Method, Status: statusCode(c, err), Duration: time.Since(started), RequestBytes: c.Request().ContentLength})
		}
		return err
	}
}
func organizationID(c echo.Context) uuid.UUID {
	if value, ok := c.Get(organizationContextKey).(uuid.UUID); ok {
		return value
	}
	if parsed, err := uuid.FromString(strings.TrimSpace(c.QueryParam("client_id"))); err == nil {
		return parsed
	}
	if strings.HasPrefix(strings.ToLower(c.Path()), "/api/clients/:id") {
		if parsed, err := uuid.FromString(strings.TrimSpace(c.Param("id"))); err == nil {
			return parsed
		}
	}
	if value, ok := c.Get(applicationaccess.ContextOrganizationID).(uuid.UUID); ok {
		return value
	}
	return uuid.Nil
}
func statusCode(c echo.Context, err error) int {
	if err != nil {
		if httpError, ok := err.(*echo.HTTPError); ok {
			return httpError.Code
		}
		return http.StatusInternalServerError
	}
	if c.Response().Status > 0 {
		return c.Response().Status
	}
	return http.StatusOK
}
