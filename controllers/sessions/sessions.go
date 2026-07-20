package sessions

import (
	"errors"
	"events-stocks/internal/authz"
	"events-stocks/internal/organizationcontext"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/models"
	"events-stocks/services/applications"
	"events-stocks/services/users"
	"events-stocks/utils"
	"net/http"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var sessionService *applications.SessionService

func Configure(service *applications.SessionService) {
	sessionService = service
}

func GetSession(c echo.Context) error {
	session, ok := c.Get(applicationaccess.ContextKey).(*applications.Session)
	if !ok || session == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Application session unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Application session", session)
}

func IssueOrganizationContext(c echo.Context) error {
	session, ok := c.Get(applicationaccess.ContextKey).(*applications.Session)
	if !ok || session == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Application session unavailable", "")
	}
	var request struct {
		OrganizationID string `json:"organization_id"`
	}
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid organization context", err.Error())
	}
	organizationID, err := uuid.FromString(request.OrganizationID)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid organization context", "organization_id must be a UUID")
	}
	if !session.AllowsOrganization(organizationID) {
		return utils.Error(c, http.StatusForbidden, "Organization context denied", "organization is not enabled for this application session")
	}
	sub, _ := c.Get("cognito_sub").(string)
	token, expiresAt, err := organizationcontext.Generate(sub, session.Application.Code, organizationID, organizationcontext.DefaultTTL)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Organization context unavailable", "credential could not be issued")
	}
	return utils.Success(c, http.StatusOK, "Organization context created", map[string]any{
		"token":           token,
		"expires_at":      expiresAt,
		"organization_id": organizationID,
	})
}

func ListMemberApplications(c echo.Context) error {
	clientID, userID, ok := memberApplicationIDs(c)
	if !ok {
		return nil
	}
	currentUser, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if err := authz.RequireClientCapability(currentUser, clientID, authz.CapabilityMembersManage); err != nil {
		return authz.Respond(c, err)
	}
	if operationalRootCannotManageApplicationTarget(c, currentUser, userID) {
		return nil
	}
	access, err := sessionService.ListMemberApplications(clientID, userID)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Could not list application access", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Member application access", access)
}

func SetMemberApplication(c echo.Context) error {
	clientID, userID, ok := memberApplicationIDs(c)
	if !ok {
		return nil
	}
	currentUser, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if err := authz.RequireClientCapability(currentUser, clientID, authz.CapabilityMembersManage); err != nil {
		return authz.Respond(c, err)
	}
	if operationalRootCannotManageApplicationTarget(c, currentUser, userID) {
		return nil
	}
	var request struct {
		IsActive bool `json:"is_active"`
	}
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid application access", err.Error())
	}
	err = sessionService.SetMemberApplicationAccess(clientID, userID, c.Param("application_code"), request.IsActive)
	if err != nil {
		if errors.Is(err, applications.ErrApplicationNotEnabled) {
			return utils.Error(c, http.StatusConflict, "Application is not enabled", err.Error())
		}
		return utils.Error(c, http.StatusBadRequest, "Could not update application access", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Member application access updated", map[string]bool{"is_active": request.IsActive})
}

func operationalRootCannotManageApplicationTarget(c echo.Context, requester *models.User, targetUserID uuid.UUID) bool {
	if requester == nil || requester.EffectiveRootLevel() != models.RootLevelOperational {
		return false
	}
	target, err := users.GetUserByID(targetUserID)
	if err != nil {
		_ = utils.Error(c, http.StatusNotFound, "User not found", err.Error())
		return true
	}
	if target.IsPlatformAdmin() {
		_ = utils.Error(c, http.StatusForbidden, "Forbidden", "Operational roots cannot inspect or change platform-root application access")
		return true
	}
	return false
}

func memberApplicationIDs(c echo.Context) (uuid.UUID, uuid.UUID, bool) {
	if sessionService == nil {
		_ = utils.Error(c, http.StatusServiceUnavailable, "Application access unavailable", "")
		return uuid.Nil, uuid.Nil, false
	}
	clientID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		_ = utils.Error(c, http.StatusBadRequest, "Invalid organization", err.Error())
		return uuid.Nil, uuid.Nil, false
	}
	userID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		_ = utils.Error(c, http.StatusBadRequest, "Invalid user", err.Error())
		return uuid.Nil, uuid.Nil, false
	}
	return clientID, userID, true
}
