package clients

import (
	"events-stocks/configuration/constants"
	"events-stocks/models"
	"events-stocks/repositories/clientrepository"
	"events-stocks/services/clients"
	resourceService "events-stocks/services/resources"
	"events-stocks/services/users"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

var clientSvc *clients.ClientService

func InitClientsController(svc *clients.ClientService) {
	clientSvc = svc
}

func CreateNewClient(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}
	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	name := c.FormValue("name")
	clientTypeIDStr := c.FormValue("client_type_id")
	parentIDStr := c.FormValue("parent_id")

	if name == "" {
		return utils.Error(c, http.StatusBadRequest, "Name required", "Client name cannot be empty")
	}

	clientTypeID, err := uuid.FromString(clientTypeIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "client_type_id must be a valid UUID")
	}

	var parentID *uuid.UUID
	if parentIDStr != "" && parentIDStr != "null" {
		u, err := uuid.FromString(parentIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid parent_id", "parent_id must be a valid UUID")
		}
		parentID = &u
	}

	file, header, err := c.Request().FormFile("logo")
	if err == nil {
		defer file.Close()
	}

	newClient, err := clientSvc.CreateClientWithLogo(name, clientTypeID, user.ID, parentID, file, header)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to create client", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Client created", newClient)
}

func GetClient(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	clientIDStr := c.Param("id")
	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Client ID", err.Error())
	}

	client, err := clientSvc.GetClientDetails(clientID, user.ID)
	if err != nil {
		if err.Error() == "access denied: user is not a member of this client hierarchy" {
			return utils.Error(c, http.StatusForbidden, "Access Denied", "You do not have permission to view this organization")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error fetching client", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Client details", client)
}

func ListMyClients(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	myClients, err := clientSvc.GetMyClients(user.ID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching clients", err.Error())
	}

	cfg, ok := c.Get("config").(*models.Config)
	if !ok {
		return utils.Error(c, http.StatusInternalServerError, "Config Error", "No se pudo recuperar la configuración del sistema")
	}

	for i := range myClients {
		if myClients[i].Logo != "" {
			fullPath := fmt.Sprintf("clients/%s/logo/%s", myClients[i].ID, myClients[i].Logo)
			url, _ := resourceService.GetPresignedURL(
				fullPath,
				cfg.AwsBucketName,
				constants.DefaultCloudProvider,
			)
			myClients[i].Logo = url
		}
	}

	return utils.Success(c, http.StatusOK, "User clients", myClients)
}

func GetMySubClients(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	parentIDStr := c.QueryParam("parent_id")
	if parentIDStr == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing parent_id", "You must provide parent_id query parameter")
	}

	parentID, err := uuid.FromString(parentIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Parent ID", err.Error())
	}

	children, err := clientSvc.GetClientChildren(parentID, user.ID)
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Access Denied", "You do not have permission to view children of this organization")
	}

	return utils.Success(c, http.StatusOK, "Sub-clients list", children)
}

func InviteUser(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}
	requester, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	var req struct {
		ClientID uuid.UUID `json:"client_id"`
		UserID   uuid.UUID `json:"user_id"`
		RoleID   uuid.UUID `json:"role_id"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	allowed, role := clientrepository.CheckAccessRecursive(requester.ID, req.ClientID)
	if !allowed || (role != "OWNER" && role != "ADMIN" && role != "INHERITED_OWNER") {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", "You cannot add members to this client")
	}

	if err := clientSvc.AddUserToClient(req.ClientID, req.UserID, req.RoleID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to add user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User added to client", nil)
}

func DeleteClient(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	clientIDStr := c.Param("id")
	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Client ID", err.Error())
	}

	err = clientSvc.DeleteClient(clientID, user.ID)
	if err != nil {
		if err.Error() == "permission denied: only the owner can delete the organization" {
			return utils.Error(c, http.StatusForbidden, "Permission Denied", err.Error())
		}
		return utils.Error(c, http.StatusInternalServerError, "Error deleting client", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Client deleted successfully", nil)
}

// CreateClientMember registers a new user and links them to a client.
// Cross-domain: uses users package-level functions for user creation.
func CreateClientMember(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}

	requester, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	var req struct {
		Email     string    `json:"email"`
		Password  string    `json:"password"`
		FirstName string    `json:"first_name"`
		LastName  string    `json:"last_name"`
		ClientID  uuid.UUID `json:"client_id"`
		RoleID    uuid.UUID `json:"role_id"`
	}

	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	allowed, role := clientrepository.CheckAccessRecursive(requester.ID, req.ClientID)
	if !allowed || (role != "OWNER" && role != "ADMIN" && role != "INHERITED_OWNER") {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", "You cannot add members to this client")
	}

	newUser, err := users.RegisterUser(req.Email, req.Password, req.FirstName, req.LastName)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Registration failed", err.Error())
	}

	err = clientSvc.AddUserToClient(req.ClientID, newUser.ID, req.RoleID)
	if err != nil {
		_ = users.DeleteFullAccount(newUser.CognitoSub)
		return utils.Error(c, http.StatusInternalServerError, "Failed to link user to client", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Member created and linked successfully", map[string]interface{}{
		"user_id":   newUser.ID,
		"client_id": req.ClientID,
		"email":     newUser.Email,
		"role_id":   req.RoleID,
	})
}

func ListClientMembers(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	targetClientIDStr := c.QueryParam("client_id")
	if targetClientIDStr == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing client_id", "You must specify which client to list")
	}

	targetClientID, err := uuid.FromString(targetClientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	members, err := clientSvc.ListClientMembers(targetClientID, user.ID)
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Access Denied", err.Error())
	}

	var response []map[string]interface{}
	for _, m := range members {
		response = append(response, map[string]interface{}{
			"user_id":       m.UserID,
			"first_name":    m.User.FirstName,
			"last_name":     m.User.LastName,
			"email":         m.User.Email,
			"profile_image": m.User.ProfileImage,
			"role_id":       m.ClientRoleID,
			"role_name":     m.ClientRole.Name,
			"joined_at":     m.CreatedAt,
		})
	}

	return utils.Success(c, http.StatusOK, "Client members list", response)
}

func RemoveMember(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	requester, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	targetUserID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid user ID", err.Error())
	}

	clientID, err := uuid.FromString(c.QueryParam("client_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}

	if err := clientSvc.RemoveClientMember(clientID, requester.ID, targetUserID); err != nil {
		return utils.Error(c, http.StatusForbidden, "Operation failed", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Member removed successfully", nil)
}

func UpdateMemberRole(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	requester, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	targetUserID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid user ID", err.Error())
	}

	clientID, err := uuid.FromString(c.QueryParam("client_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}

	var req struct {
		NewRoleID uuid.UUID `json:"new_role_id"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	if err := clientSvc.UpdateClientMemberRole(clientID, requester.ID, targetUserID, req.NewRoleID); err != nil {
		return utils.Error(c, http.StatusForbidden, "Operation failed", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Member role updated", nil)
}

func UpdateClient(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Session expired")
	}

	clientID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}
	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	name := c.FormValue("name")
	file, header, errFile := c.Request().FormFile("logo")

	cfg, ok := c.Get("config").(*models.Config)
	if !ok || cfg == nil {
		return utils.Error(c, http.StatusInternalServerError, "Config Error", "missing config")
	}
	rs := resourceService.NewResourceService(cfg)

	var logoName string
	var shouldDeleteOld = false

	if errFile == nil {
		defer file.Close()
		filename, _, err := rs.UploadClientLogo(file, header, clientID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error uploading logo", err.Error())
		}
		logoName = filename
		shouldDeleteOld = true
	} else if c.FormValue("remove_logo") == "true" {
		logoName = ""
		shouldDeleteOld = true
	} else {
		current, err := clientrepository.GetClientByID(clientID)
		if err != nil {
			return utils.Error(c, http.StatusNotFound, "Client not found", "")
		}
		logoName = current.Logo
	}

	res, err := clientSvc.UpdateClientDetails(clientID, user.ID, name, logoName, shouldDeleteOld)
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Update error", err.Error())
	}

	if res.Logo != "" {
		fullPath := fmt.Sprintf("clients/%s/logo/%s", res.ID, res.Logo)
		url, err := resourceService.GetPresignedURL(fullPath, cfg.AwsBucketName, constants.DefaultCloudProvider)
		if err == nil {
			res.Logo = url
		}
	}

	return utils.Success(c, http.StatusOK, "Organization updated successfully", res)
}
