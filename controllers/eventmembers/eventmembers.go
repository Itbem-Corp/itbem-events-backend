package eventmembers

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	eventmembersService "events-stocks/services/eventmembers"
	"events-stocks/utils"
	"net/http"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var service *eventmembersService.EventMemberService

func InitEventMembersController(memberService *eventmembersService.EventMemberService) {
	service = memberService
}

func Capabilities(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, _, err := authz.RequireEventAccess(c, eventID); err != nil {
		return authz.Respond(c, err)
	}
	capabilities := map[string]bool{}
	for _, capability := range []authz.Capability{
		authz.CapabilityEventManage,
		authz.CapabilityEventDelete,
		authz.CapabilityGuestManage,
		authz.CapabilityCheckin,
		authz.CapabilityAnalyticsView,
		authz.CapabilityMembersManage,
	} {
		_, _, capabilityErr := authz.RequireEventCapability(c, eventID, capability)
		capabilities[string(capability)] = capabilityErr == nil
	}
	return utils.Success(c, http.StatusOK, "Event capabilities", capabilities)
}

func List(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, _, err := authz.RequireEventCapability(c, eventID, authz.CapabilityMembersManage); err != nil {
		return authz.Respond(c, err)
	}
	members, err := service.List(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Could not load event members", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event members", dtos.NewEventMemberResponses(members))
}

func Upsert(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	_, event, err := authz.RequireEventCapability(c, eventID, authz.CapabilityMembersManage)
	if err != nil {
		return authz.Respond(c, err)
	}
	var body struct {
		UserID uuid.UUID `json:"user_id"`
		Role   string    `json:"role"`
	}
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}
	member, err := service.Upsert(eventID, event.ClientID, body.UserID, body.Role)
	if errors.Is(err, eventmembersService.ErrInvalidMemberAssignment) {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "user_id, organization and a valid event role are required")
	}
	if errors.Is(err, eventmembersService.ErrOrganizationMembership) {
		return utils.Error(c, http.StatusBadRequest, "Invalid member", "user must have organization access before event assignment")
	}
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Could not save event member", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event member saved", dtos.NewEventMemberResponse(*member))
}

func Remove(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	userID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid user ID", err.Error())
	}
	if _, _, err := authz.RequireEventCapability(c, eventID, authz.CapabilityMembersManage); err != nil {
		return authz.Respond(c, err)
	}
	if err := service.Remove(eventID, userID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Could not remove event member", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event member removed", nil)
}
