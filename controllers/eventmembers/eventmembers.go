package eventmembers

import (
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/repositories/eventmemberrepository"
	clientsService "events-stocks/services/clients"
	"events-stocks/utils"
	"net/http"
	"strings"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var repo *eventmemberrepository.EventMemberRepo
var clientSvc *clientsService.ClientService

func InitEventMembersController(memberRepo *eventmemberrepository.EventMemberRepo, clients *clientsService.ClientService) {
	repo = memberRepo
	clientSvc = clients
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
	members, err := repo.List(eventID)
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
	role := strings.ToUpper(strings.TrimSpace(body.Role))
	if body.UserID == uuid.Nil || !validRole(role) {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "user_id and a valid event role are required")
	}
	if event.ClientID == nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event", "event must belong to an organization")
	}
	if _, err := clientSvc.GetClientDetails(*event.ClientID, body.UserID); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid member", "user must have organization access before event assignment")
	}
	member, err := repo.Upsert(eventID, body.UserID, role)
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
	if err := repo.Remove(eventID, userID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Could not remove event member", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event member removed", nil)
}

func validRole(role string) bool {
	switch role {
	case "EVENT_OWNER", "MANAGER", "EDITOR", "CHECKIN", "ANALYST", "VIEWER":
		return true
	default:
		return false
	}
}
