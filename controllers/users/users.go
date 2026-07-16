package users

import (
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/tenantresources"
	"events-stocks/models"
	resourcesService "events-stocks/services/resources"
	"events-stocks/services/users"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

var userSvc *users.UserService
var adminSvc *users.AdminUserService
var resourceSvc *resourcesService.ResourceService

func InitUsersController(svc *users.UserService, admin *users.AdminUserService, resources *resourcesService.ResourceService) {
	userSvc = svc
	adminSvc = admin
	resourceSvc = resources
}

func profileImageViewURL(path string, buckets ...string) string {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" || resourceSvc == nil {
		return ""
	}
	svc := resourceSvc
	if len(buckets) > 0 {
		svc = svc.WithBucket(buckets[0])
	}
	viewURL, _ := svc.GetAvatarPresignedURL(cleanPath)
	return viewURL
}

func userProfileResponse(user *models.User) dtos.UserProfileResponse {
	if user == nil {
		return dtos.UserProfileResponse{}
	}
	return dtos.NewUserProfileResponse(user, profileImageViewURL(user.ProfileImage, user.ProfileImageBucket))
}

func clientLogoViewURL(clientID uuid.UUID, logo string) string {
	cleanLogo := strings.TrimSpace(logo)
	if cleanLogo == "" || strings.HasPrefix(cleanLogo, "http://") || strings.HasPrefix(cleanLogo, "https://") {
		return cleanLogo
	}
	if resourceSvc == nil {
		return cleanLogo
	}
	return resourceSvc.GetClientLogoURL(clientID, cleanLogo)
}

func signClientSummaryLogo(client *dtos.ClientSummaryResponse) {
	if client == nil {
		return
	}
	client.Logo = clientLogoViewURL(client.ID, client.Logo)
}

func signClientResponseLogo(client *dtos.ClientResponse) {
	if client == nil {
		return
	}
	client.Logo = clientLogoViewURL(client.ID, client.Logo)
	signClientSummaryLogo(client.Parent)
	for i := range client.Children {
		signClientSummaryLogo(&client.Children[i])
	}
}

func signClientResponseLogos(clients []dtos.ClientResponse) {
	for i := range clients {
		signClientResponseLogo(&clients[i])
	}
}

// UpdateUser updates the authenticated user's text data (First/Last name)
func UpdateUser(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}

	user, err := userSvc.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	updatedUser, err := userSvc.UpdateUserInformation(user.ID, req.FirstName, req.LastName)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating profile", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Profile updated", userProfileResponse(updatedUser))
}

// GetUser returns the full profile of the authenticated user
func GetUser(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	user, err := userSvc.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User profile", userProfileResponse(user))
}

// DeleteUser deletes the current user's account
func DeleteUser(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}

	if err := userSvc.DeleteFullAccount(cognitoSub); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting account", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Account deleted", nil)
}

// UploadAvatar uploads a new avatar for the authenticated user
func UploadAvatar(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}

	user, err := userSvc.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	file, header, err := c.Request().FormFile("avatar")
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Error reading file", "Field 'avatar' required")
	}
	defer file.Close()

	if resourceSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Resource service not initialized", "")
	}

	bucket, bucketErr := tenantresources.BucketFromContext(c)
	if bucketErr != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Tenant storage is not configured", bucketErr.Error())
	}
	scopedResourceSvc := resourceSvc.WithBucket(bucket)
	newAvatarPath, err := scopedResourceSvc.UploadAvatar(file, header, user.ID)
	if err != nil {
		status, detail := resourcesService.UploadErrorResponse(err)
		return utils.Error(c, status, "Error uploading avatar", detail)
	}

	if err := userSvc.UpdateProfileImageInBucket(user.ID, newAvatarPath, bucket); err != nil {
		if cleanupErr := scopedResourceSvc.DeleteObjectByPath(newAvatarPath); cleanupErr != nil {
			slog.Error("new avatar rollback failed", "user_id", user.ID, "path", newAvatarPath, "error", cleanupErr)
		}
		return utils.Error(c, http.StatusInternalServerError, "Error saving changes", err.Error())
	}
	if oldAvatarPath := strings.TrimSpace(user.ProfileImage); oldAvatarPath != "" && oldAvatarPath != newAvatarPath {
		if cleanupErr := resourceSvc.WithBucket(user.ProfileImageBucket).DeleteObjectByPath(oldAvatarPath); cleanupErr != nil {
			slog.Warn("old avatar cleanup failed", "user_id", user.ID, "path", oldAvatarPath, "error", cleanupErr)
		}
	}

	signedURL, _ := scopedResourceSvc.GetAvatarPresignedURL(newAvatarPath)

	return utils.Success(c, http.StatusOK, "Avatar updated", dtos.AvatarResponse{
		Path: newAvatarPath,
		URL:  signedURL,
	})
}

func DeleteAvatar(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
	}

	user, err := userSvc.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	if user.ProfileImage == "" {
		return utils.Success(c, http.StatusOK, "No avatar to delete", nil)
	}

	if err := resourceSvc.WithBucket(user.ProfileImageBucket).DeleteObjectByPath(user.ProfileImage); err != nil {
		slog.Warn("avatar cleanup failed", "user_id", user.ID, "path", user.ProfileImage, "error", err)
	}

	if err := userSvc.ClearProfileImage(user.ID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error cleaning avatar", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Avatar deleted", nil)
}

// requireRoot writes an auth/forbidden response and returns false if the requester
// is not a root user. Returns true if the check passed.
func requireRoot(c echo.Context) bool {
	if _, err := authz.RequireRoot(c); err != nil {
		_ = authz.Respond(c, err)
		return false
	}
	return true
}

func requirePrimaryRoot(c echo.Context) bool {
	if _, err := authz.RequirePrimaryRoot(c); err != nil {
		_ = authz.Respond(c, err)
		return false
	}
	return true
}

// requireManageableUser allows an operational root to support standard users
// while protecting every platform-root account from level-2 changes.
func requireManageableUser(c echo.Context, targetID uuid.UUID) bool {
	requester, err := authz.RequireRoot(c)
	if err != nil {
		_ = authz.Respond(c, err)
		return false
	}
	if requester.IsPrimaryRoot() {
		return true
	}
	target, err := adminSvc.GetUserSummary(targetID)
	if err != nil {
		_ = utils.Error(c, http.StatusNotFound, "User not found", err.Error())
		return false
	}
	if target.IsRoot || target.RootLevel > models.RootLevelNone {
		_ = utils.Error(c, http.StatusForbidden, "Forbidden", "Operational roots cannot modify platform-root accounts")
		return false
	}
	return true
}

// UpdateUserRootLevel grants or revokes constrained operational platform
// access. It intentionally cannot create another primary root.
func UpdateUserRootLevel(c echo.Context) error {
	if !requirePrimaryRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}
	var req struct {
		RootLevel int `json:"root_level"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}
	if err := userSvc.SetUserRootLevel(userID, req.RootLevel); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Could not update root level", err.Error())
	}
	user, err := adminSvc.GetUserSummary(userID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "User not found", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Root level updated", user)
}

func ListAllUsers(c echo.Context) error {
	requester, err := authz.RequireRoot(c)
	if err != nil {
		_ = authz.Respond(c, err)
		return nil
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	pageSize, _ := strconv.Atoi(c.QueryParam("page_size"))
	status := c.QueryParam("status")
	// A Root 2 account uses this directory to support ordinary users. It must
	// never enumerate platform administrators, including by requesting the
	// root-only filter explicitly.
	if !requester.IsPrimaryRoot() {
		status = "non_root"
	}
	list, err := adminSvc.ListAllUsers(dtos.AdminUsersListQuery{
		Page:     page,
		PageSize: pageSize,
		Search:   c.QueryParam("search"),
		Status:   status,
	})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error listing users", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Users", list)
}

func GetUserDetail(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	idParam := c.Param("id")

	userID, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}
	// Root 2 is a support operator, not a peer of platform administrators.
	// Do this before loading the detailed profile so a support account cannot
	// inspect another root's organization assignments or profile metadata.
	if !requireManageableUser(c, userID) {
		return nil
	}

	var user dtos.AdminUserDetailResponse
	if c.QueryParam("include_clients") == "false" {
		user, err = adminSvc.GetUserSummary(userID)
	} else {
		user, err = adminSvc.GetUserDetail(userID)
	}
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "User not found", err.Error())
	}
	signClientResponseLogos(user.Clients)

	return utils.Success(c, http.StatusOK, "User", user)
}

func ListUserClients(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}
	// Client memberships reveal the platform's account topology. Root 2 may
	// assist ordinary users but must not enumerate a root account's access.
	if !requireManageableUser(c, userID) {
		return nil
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
		response, listErr := adminSvc.GetUserClientsPage(userID, dtos.ClientsListQuery{Page: page, PageSize: pageSize, Search: c.QueryParam("search")})
		if listErr != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error retrieving user clients", listErr.Error())
		}
		signClientResponseLogos(response.Data)
		return utils.Success(c, http.StatusOK, "User clients", response)
	}

	clients, err := adminSvc.ListUserClients(userID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error retrieving user clients", err.Error())
	}
	response := dtos.NewClientResponses(clients)
	signClientResponseLogos(response)

	return utils.Success(c, http.StatusOK, "User clients", response)
}

func UpdateUserDetail(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}
	if !requireManageableUser(c, userID) {
		return nil
	}

	var req struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	user, err := adminSvc.UpdateUserInformation(userID, req.FirstName, req.LastName)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User updated", dtos.NewAdminUserResponse(user))
}

func DeleteUserDetail(c echo.Context) error {
	if !requirePrimaryRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}

	if err := adminSvc.DeleteUser(userID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User deleted", nil)
}

func DeactivateUser(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}
	if !requireManageableUser(c, userID) {
		return nil
	}

	user, err := adminSvc.SetUserActive(userID, false)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to deactivate user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User deactivated", dtos.NewAdminUserResponse(user))
}

func ActivateUser(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}
	if !requireManageableUser(c, userID) {
		return nil
	}

	user, err := adminSvc.SetUserActive(userID, true)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to activate user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User activated", dtos.NewAdminUserResponse(user))
}

func InviteUser(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}

	var req struct {
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}

	if err := c.Bind(&req); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid data", err.Error())
	}

	user, err := adminSvc.InviteUser(req.Email, req.FirstName, req.LastName)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Invite failed", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Invitation sent", dtos.NewAdminUserResponse(user))
}
