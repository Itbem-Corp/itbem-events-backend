package audit

import (
	"events-stocks/configuration"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

const maxUserAgentLength = 512

// contextSkipOnSuccess marks a request whose successful outcome changes no
// state. It is honoured only on skipOnSuccessRoute.
const contextSkipOnSuccess = "audit_skip_on_success"

// skipOnSuccessRoute is the only route whose successful outcome may skip the
// audit row: renewing the organization credential already in use.
const skipOnSuccessRoute = "/api/session/organization-context"

// SkipOnSuccess tells Mutations that this request's successful outcome changes
// no state, so it needs no audit row. Denials and errors are still recorded.
// The mark is honoured only on POST /api/session/organization-context, so no
// other handler can use it to hide a state change.
func SkipOnSuccess(c echo.Context) {
	c.Set(contextSkipOnSuccess, true)
}

// SkipOnSuccessRequested reports whether the handler marked the request with
// SkipOnSuccess. Whether the mark is honoured is decided by Mutations.
func SkipOnSuccessRequested(c echo.Context) bool {
	marked, _ := c.Get(contextSkipOnSuccess).(bool)
	return marked
}

// Mutations persists a body-free, append-only audit record for every
// authenticated state-changing request and every denied authenticated read.
// The historical name is retained because routes already register it.
func Mutations(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		err := next(c)
		status := responseStatus(c, err)
		if !shouldPersist(c, c.Request().Method, status) {
			return err
		}
		entry := newEntry(c, status)
		if configuration.DB == nil {
			slog.Error("security audit unavailable", "request_id", entry.RequestID, "route", entry.Route)
			return err
		}
		if writeErr := configuration.DB.Create(entry).Error; writeErr != nil {
			slog.Error(
				"security audit persistence failed",
				"request_id", entry.RequestID,
				"tenant", entry.TenantCode,
				"route", entry.Route,
				"status", entry.Status,
				"error", writeErr,
			)
		}
		return err
	}
}

// shouldPersist applies the audit policy plus the single no-op exception: a
// successful POST /api/session/organization-context that its handler marked
// with SkipOnSuccess. The route check keeps the mark from hiding any other
// state change.
func shouldPersist(c echo.Context, method string, status int) bool {
	if !shouldAudit(method, status) {
		return false
	}
	succeeded := status >= 200 && status < 400
	renewal := strings.EqualFold(strings.TrimSpace(method), http.MethodPost) && c.Path() == skipOnSuccessRoute
	return !succeeded || !renewal || !SkipOnSuccessRequested(c)
}

func shouldAudit(method string, status int) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func responseStatus(c echo.Context, err error) int {
	if err != nil {
		if httpError, ok := err.(*echo.HTTPError); ok {
			return httpError.Code
		}
		return http.StatusInternalServerError
	}
	if status := c.Response().Status; status > 0 {
		return status
	}
	return http.StatusOK
}

func newEntry(c echo.Context, status int) *models.AuditLog {
	entry := &models.AuditLog{
		ActorCognitoSub: stringContext(c, "cognito_sub"),
		TenantCode:      stringContext(c, "tenant_code"),
		Method:          strings.ToUpper(c.Request().Method),
		Route:           auditRoute(c),
		ResourceType:    resourceType(c),
		ResourceID:      resourceID(c),
		Status:          status,
		Succeeded:       status >= 200 && status < 400,
		RequestID:       c.Response().Header().Get(echo.HeaderXRequestID),
		ClientIP:        c.RealIP(),
		UserAgent:       bounded(c.Request().UserAgent(), maxUserAgentLength),
	}
	if session, ok := c.Get(applicationaccess.ContextKey).(*applications.Session); ok && session != nil {
		actorUserID := session.User.ID
		entry.ActorUserID = &actorUserID
	}
	return entry
}

func resourceType(c echo.Context) string {
	route := strings.Trim(strings.TrimPrefix(auditRoute(c), "/api/"), "/")
	if route == "" {
		return ""
	}
	return bounded(strings.Split(route, "/")[0], 64)
}

func resourceID(c echo.Context) string {
	names := c.ParamNames()
	values := c.ParamValues()
	for i, name := range names {
		if i >= len(values) {
			break
		}
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized == "id" || strings.HasSuffix(normalized, "_id") || strings.HasSuffix(normalized, "id") {
			return bounded(strings.TrimSpace(values[i]), 128)
		}
	}
	return ""
}

func auditRoute(c echo.Context) string {
	if route := strings.TrimSpace(c.Path()); route != "" {
		return bounded(route, 512)
	}
	return "/<unmatched>"
}

func stringContext(c echo.Context, key string) string {
	value, _ := c.Get(key).(string)
	return bounded(strings.TrimSpace(value), 128)
}

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
