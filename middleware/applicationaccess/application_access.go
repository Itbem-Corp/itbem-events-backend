package applicationaccess

import (
	"errors"
	"events-stocks/internal/organizationcontext"
	"events-stocks/internal/products"
	requestcontract "events-stocks/internal/requestcontext"
	"events-stocks/services/applications"
	"events-stocks/utils"
	"net/http"
	"strings"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const ContextKey = "application_session"

const (
	HeaderApplicationCode     = requestcontract.HeaderApplicationCode
	HeaderWorkspaceMode       = requestcontract.HeaderWorkspaceMode
	HeaderOrganizationID      = requestcontract.HeaderOrganizationID
	HeaderOrganizationContext = requestcontract.HeaderOrganizationContext
	ContextWorkspaceMode      = "workspace_mode"
	ContextOrganizationID     = "organization_id"
)

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
		if err := validateRequestContext(c, tenant, session); err != nil {
			return utils.Error(c, http.StatusForbidden, "Application context denied", err.Error())
		}
		if products.RequiresEventOperationsPath(c.Request().URL.Path) && !products.SupportsEventOperations(tenant) {
			return utils.Error(c, http.StatusForbidden, "Application surface denied", "Event operations are only available in the EventiApp application")
		}
		if required := requiredSurfaceCapability(c.Request().Method, c.Request().URL.Path); required != "" &&
			!session.HasAnyCapability(strings.Split(required, "|")...) {
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

func validateRequestContext(c echo.Context, tenant string, session *applications.Session) error {
	if session == nil {
		return errors.New("application session does not match the authenticated tenant")
	}
	resolved, err := requestcontract.Resolve(requestcontract.Input{
		AuthenticatedApplication: tenant,
		RequestedApplication:     c.Request().Header.Get(HeaderApplicationCode),
		SessionApplication:       session.Application.Code,
		RequestedWorkspaceMode:   c.Request().Header.Get(HeaderWorkspaceMode),
		RequestedOrganizationID:  c.Request().Header.Get(HeaderOrganizationID),
		AllowsPlatformAdmin:      session.Application.AllowsPlatformAdmin,
		IsRoot:                   session.User.IsRoot,
		OrganizationAllowed: func(candidate uuid.UUID) bool {
			return session.AllowsOrganization(candidate)
		},
	})
	if err != nil {
		return err
	}
	c.Set(ContextWorkspaceMode, resolved.WorkspaceMode)
	if resolved.HasOrganization {
		contextToken := strings.TrimSpace(c.Request().Header.Get(HeaderOrganizationContext))
		if contextToken == "" && requiresOrganizationCredential(c.Request().URL.Path) {
			return errors.New("organization context token is required")
		}
		if contextToken != "" && !organizationcontext.Validate(contextToken, stringContext(c, "cognito_sub"), tenant, resolved.OrganizationID) {
			return errors.New("organization context token is invalid or expired")
		}
		c.Set(ContextOrganizationID, resolved.OrganizationID)
	} else if strings.TrimSpace(c.Request().Header.Get(HeaderOrganizationContext)) != "" {
		return errors.New("organization context token requires an organization workspace")
	}
	return nil
}

// Session bootstrap and credential issuance must remain reachable when a
// credential is absent or expired. Both are still protected by Cognito and the
// application membership resolved above; neither exposes organization data.
func requiresOrganizationCredential(path string) bool {
	path = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(path)), "/")
	return path != "/api/session" && path != "/api/session/organization-context"
}

func stringContext(c echo.Context, key string) string {
	value, _ := c.Get(key).(string)
	return value
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
	if strings.HasPrefix(path, "/api/metrics") {
		return "metrics:view"
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
	if products.RequiresEventOperationsPath(path) {
		return "events:view"
	}
	return ""
}
