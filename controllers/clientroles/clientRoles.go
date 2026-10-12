package clientroles

import (
	"net/http"
	"strings"

	"events-stocks/dtos"
	"events-stocks/internal/authz"
	clientrolesService "events-stocks/services/clientroles"
	clientsService "events-stocks/services/clients"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

var (
	clientSvc     *clientsService.ClientService
	clientRoleSvc *clientrolesService.ClientRoleService
)

func InitClientRolesController(clientService *clientsService.ClientService, roleService *clientrolesService.ClientRoleService) {
	clientSvc = clientService
	clientRoleSvc = roleService
}

// ListClientRoles returns the roles that the current user can assign.
// GET /catalogs/roles?client_id=UUID
func ListClientRoles(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}

	clientIDStr := c.QueryParam("client_id")
	if clientIDStr == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing client_id", "You must provide client_id to filter allowed roles")
	}

	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if clientSvc == nil || clientRoleSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Service unavailable", "")
	}

	// Both platform-root levels manage teams in any organization. They start
	// from the roles below Owner and keep only those within their own
	// capabilities, matching CanAssignClientRoleAsRoot: Root 1 keeps them all,
	// Root 2 never sees Admin, Event manager or Editor.
	myRoleCode := "OWNER"
	if !user.IsPlatformAdmin() {
		myRoleCode, err = clientSvc.GetEffectiveMemberRole(user.ID, clientID)
		if err != nil {
			return utils.Error(c, http.StatusForbidden, "Access Denied", "You are not a member of this organization")
		}
	}
	myRoleCode = strings.TrimPrefix(strings.ToUpper(myRoleCode), "INHERITED_")

	roles, err := clientRoleSvc.GetAllowedRolesToAssign(myRoleCode)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching roles", err.Error())
	}
	if user.IsPlatformAdmin() {
		grantable := roles[:0]
		for _, role := range roles {
			if authz.PlatformRootMayGrantRole(user, role.Code) {
				grantable = append(grantable, role)
			}
		}
		roles = grantable
	}

	return utils.Success(c, http.StatusOK, "Allowed roles", dtos.NewClientRoleResponses(roles))
}
