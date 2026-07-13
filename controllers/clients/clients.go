package clients

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/clients"
	resourcesService "events-stocks/services/resources"
	"events-stocks/services/users"
	validations "events-stocks/services/validations"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strconv"
	"strings"
)

var clientSvc *clients.ClientService

func InitClientsController(svc *clients.ClientService) {
	clientSvc = svc
}

func operationalRootMayNotManageOrganization(c echo.Context, user *models.User) bool {
	if user.EffectiveRootLevel() != models.RootLevelOperational {
		return false
	}
	_ = utils.Error(c, http.StatusForbidden, "Permission Denied", "Operational roots cannot change organization structure or teams")
	return true
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
	if operationalRootMayNotManageOrganization(c, user) {
		return nil
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
	if parentID == nil && !user.IsPrimaryRoot() {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", "Only the primary platform administrator can create a platform organization")
	}

	file, header, err := c.Request().FormFile("logo")
	if err == nil {
		defer file.Close()
	}

	var newClient *models.Client
	if user.IsPrimaryRoot() {
		newClient, err = clientSvc.CreateClientWithLogoAsPrimaryRoot(name, clientTypeID, user.ID, parentID, file, header)
	} else {
		newClient, err = clientSvc.CreateClientWithLogo(name, clientTypeID, user.ID, parentID, file, header)
	}
	if err != nil {
		status, detail := resourcesService.UploadErrorResponse(err)
		if validations.IsValidationError(err) {
			return utils.Error(c, status, "Invalid logo", detail)
		}
		return utils.Error(c, status, "Failed to create client", detail)
	}

	return utils.Success(c, http.StatusCreated, "Client created", dtos.NewClientResponse(newClient))
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

	var client *models.Client
	if user.IsPrimaryRoot() {
		client, err = clientSvc.GetClientDetailsAsPrimaryRoot(clientID)
	} else {
		client, err = clientSvc.GetClientDetails(clientID, user.ID)
	}
	if err != nil {
		if err.Error() == "access denied: user is not a member of this client hierarchy" {
			return utils.Error(c, http.StatusForbidden, "Access Denied", "You do not have permission to view this organization")
		}
		return utils.Error(c, http.StatusInternalServerError, "Error fetching client", err.Error())
	}
	if client.Logo != "" {
		client.Logo = clientSvc.GetClientLogoURL(client.ID, client.Logo)
	}

	return utils.Success(c, http.StatusOK, "Client details", dtos.NewClientResponse(client))
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
	if c.QueryParam("page_size") != "" {
		page, err := strconv.Atoi(c.QueryParam("page"))
		if err != nil || page < 1 {
			page = 1
		}
		pageSize, err := strconv.Atoi(c.QueryParam("page_size"))
		if err != nil || pageSize < 1 || pageSize > 100 {
			return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be between 1 and 100")
		}
		var userID *uuid.UUID
		if !user.IsPlatformAdmin() {
			userID = &user.ID
		}
		query := dtos.ClientsListQuery{Page: page, PageSize: pageSize, Search: strings.TrimSpace(c.QueryParam("search"))}
		myClients, total, err := clientSvc.ListClientsPaginated(userID, query)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching clients", err.Error())
		}
		for i := range myClients {
			if myClients[i].Logo != "" {
				myClients[i].Logo = clientSvc.GetClientLogoURL(myClients[i].ID, myClients[i].Logo)
			}
		}
		totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
		return utils.Success(c, http.StatusOK, "User clients", dtos.ClientsPageResponse{
			Data: dtos.NewClientResponses(myClients), Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages,
		})
	}

	var myClients []models.Client
	if user.IsPlatformAdmin() {
		myClients, err = clients.GetAllClients()
	} else {
		myClients, err = clientSvc.GetMyClients(user.ID)
	}
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching clients", err.Error())
	}

	for i := range myClients {
		if myClients[i].Logo != "" {
			myClients[i].Logo = clientSvc.GetClientLogoURL(myClients[i].ID, myClients[i].Logo)
		}
	}

	return utils.Success(c, http.StatusOK, "User clients", dtos.NewClientResponses(myClients))
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

	var children []models.Client
	if user.IsPrimaryRoot() {
		children, err = clientSvc.GetClientChildrenAsPrimaryRoot(parentID)
	} else {
		children, err = clientSvc.GetClientChildren(parentID, user.ID)
	}
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Access Denied", "You do not have permission to view children of this organization")
	}

	for i := range children {
		if children[i].Logo != "" {
			children[i].Logo = clientSvc.GetClientLogoURL(children[i].ID, children[i].Logo)
		}
	}

	return utils.Success(c, http.StatusOK, "Sub-clients list", dtos.NewClientResponses(children))
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
	if operationalRootMayNotManageOrganization(c, requester) {
		return nil
	}

	var req struct {
		ClientID uuid.UUID `json:"client_id"`
		UserID   uuid.UUID `json:"user_id"`
		Email    string    `json:"email"`
		RoleID   uuid.UUID `json:"role_id"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}
	if req.ClientID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "client_id is required")
	}
	if req.RoleID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "role_id is required")
	}

	if !requester.IsRoot {
		if err := clientSvc.CanManageClientMembers(req.ClientID, requester.ID); err != nil {
			return utils.Error(c, http.StatusForbidden, "Permission Denied", "You cannot add members to this client")
		}
	}
	var rolePermissionErr error
	if requester.IsRoot {
		rolePermissionErr = clientSvc.CanAssignClientRoleAsRoot(req.RoleID)
	} else {
		rolePermissionErr = clientSvc.CanAssignClientRole(req.ClientID, requester.ID, req.RoleID)
	}
	if rolePermissionErr != nil {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", rolePermissionErr.Error())
	}

	targetUserID := req.UserID
	if targetUserID == uuid.Nil {
		email := strings.TrimSpace(req.Email)
		if email == "" {
			return utils.Error(c, http.StatusBadRequest, "Invalid data", "user_id or email is required")
		}
		targetUser, err := users.GetUserByEmail(email)
		if err != nil {
			return utils.Error(c, http.StatusNotFound, "User not found", "No user exists with that email")
		}
		targetUserID = targetUser.ID
	}

	if err := clientSvc.AddUserToClient(req.ClientID, targetUserID, req.RoleID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to add user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User added to client", dtos.ClientMemberLinkResponse{
		UserID:   targetUserID,
		ClientID: req.ClientID,
		RoleID:   req.RoleID,
	})
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
	if operationalRootMayNotManageOrganization(c, user) {
		return nil
	}

	clientIDStr := c.Param("id")
	clientID, err := uuid.FromString(clientIDStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid Client ID", err.Error())
	}

	if user.IsPrimaryRoot() {
		err = clientSvc.DeleteClientAsPrimaryRoot(clientID)
	} else {
		err = clientSvc.DeleteClient(clientID, user.ID)
	}
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
	if operationalRootMayNotManageOrganization(c, requester) {
		return nil
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

	if !requester.IsRoot {
		if err := clientSvc.CanManageClientMembers(req.ClientID, requester.ID); err != nil {
			return utils.Error(c, http.StatusForbidden, "Permission Denied", "You cannot add members to this client")
		}
	}
	var rolePermissionErr error
	if requester.IsRoot {
		rolePermissionErr = clientSvc.CanAssignClientRoleAsRoot(req.RoleID)
	} else {
		rolePermissionErr = clientSvc.CanAssignClientRole(req.ClientID, requester.ID, req.RoleID)
	}
	if rolePermissionErr != nil {
		return utils.Error(c, http.StatusForbidden, "Permission Denied", rolePermissionErr.Error())
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

	return utils.Success(c, http.StatusCreated, "Member created and linked successfully", dtos.ClientMemberLinkResponse{
		UserID:   newUser.ID,
		ClientID: req.ClientID,
		Email:    newUser.Email,
		RoleID:   req.RoleID,
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
	if rawPageSize := c.QueryParam("page_size"); rawPageSize != "" {
		pageSize, sizeErr := strconv.Atoi(rawPageSize)
		if sizeErr != nil || pageSize < 1 {
			return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be positive")
		}
		if pageSize > 100 {
			pageSize = 100
		}
		page, pageErr := strconv.Atoi(c.QueryParam("page"))
		if pageErr != nil || page < 1 {
			page = 1
		}
		var members []models.ClientMember
		var total int64
		var listErr error
		if user.IsPrimaryRoot() {
			members, total, listErr = clientSvc.ListClientMembersPageAsRoot(targetClientID, page, pageSize, c.QueryParam("search"))
		} else {
			members, total, listErr = clientSvc.ListClientMembersPage(targetClientID, user.ID, page, pageSize, c.QueryParam("search"))
		}
		if listErr != nil {
			return utils.Error(c, http.StatusForbidden, "Access Denied", listErr.Error())
		}
		totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
		return utils.Success(c, http.StatusOK, "Client members page", dtos.ClientMembersPage{Data: dtos.NewClientMemberResponses(members), Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages})
	}

	var members []models.ClientMember
	if user.IsPrimaryRoot() {
		members, err = clientSvc.ListClientMembersAsRoot(targetClientID)
	} else {
		members, err = clientSvc.ListClientMembers(targetClientID, user.ID)
	}
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Access Denied", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Client members list", dtos.NewClientMemberResponses(members))
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
	if operationalRootMayNotManageOrganization(c, requester) {
		return nil
	}

	targetUserID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid user ID", err.Error())
	}

	clientID, err := uuid.FromString(c.QueryParam("client_id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid client ID", err.Error())
	}

	var removeErr error
	if requester.IsRoot {
		removeErr = clientSvc.RemoveClientMemberAsRoot(clientID, targetUserID)
	} else {
		removeErr = clientSvc.RemoveClientMember(clientID, requester.ID, targetUserID)
	}
	if removeErr != nil {
		return utils.Error(c, http.StatusForbidden, "Operation failed", removeErr.Error())
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
	if operationalRootMayNotManageOrganization(c, requester) {
		return nil
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
		RoleID    uuid.UUID `json:"role_id"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}
	if req.NewRoleID == uuid.Nil {
		req.NewRoleID = req.RoleID
	}
	if req.NewRoleID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", "new_role_id is required")
	}

	var updateErr error
	if requester.IsRoot {
		updateErr = clientSvc.UpdateClientMemberRoleAsRoot(clientID, targetUserID, req.NewRoleID)
	} else {
		updateErr = clientSvc.UpdateClientMemberRole(clientID, requester.ID, targetUserID, req.NewRoleID)
	}
	if updateErr != nil {
		return utils.Error(c, http.StatusForbidden, "Operation failed", updateErr.Error())
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
	if operationalRootMayNotManageOrganization(c, user) {
		return nil
	}

	name := c.FormValue("name")
	file, header, errFile := c.Request().FormFile("logo")

	var logoName string
	var shouldDeleteOld = false

	if errFile == nil {
		defer file.Close()
		filename, err := clientSvc.UploadClientLogo(file, header, clientID)
		if err != nil {
			status, detail := resourcesService.UploadErrorResponse(err)
			if validations.IsValidationError(err) {
				return utils.Error(c, status, "Invalid logo", detail)
			}
			return utils.Error(c, status, "Error uploading logo", detail)
		}
		logoName = filename
		shouldDeleteOld = true
	} else if c.FormValue("remove_logo") == "true" {
		logoName = ""
		shouldDeleteOld = true
	}

	var res *models.Client
	if user.IsPrimaryRoot() {
		res, err = clientSvc.UpdateClientDetailsAsPrimaryRoot(clientID, name, logoName, shouldDeleteOld)
	} else {
		res, err = clientSvc.UpdateClientDetails(clientID, user.ID, name, logoName, shouldDeleteOld)
	}
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Update error", err.Error())
	}

	if res.Logo != "" {
		res.Logo = clientSvc.GetClientLogoURL(res.ID, res.Logo)
	}

	return utils.Success(c, http.StatusOK, "Organization updated successfully", dtos.NewClientResponse(res))
}
