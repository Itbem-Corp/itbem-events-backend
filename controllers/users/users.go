package users

import (
	"events-stocks/configuration"
	Resources "events-stocks/services/resources"
	"events-stocks/services/users"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
	"strconv"
	"strings"
)

var userSvc *users.UserService
var adminSvc *users.AdminUserService

func InitUsersController(svc *users.UserService, admin *users.AdminUserService) {
	userSvc = svc
	adminSvc = admin
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

	return utils.Success(c, http.StatusOK, "Profile updated", map[string]interface{}{
		"first_name": updatedUser.FirstName,
		"last_name":  updatedUser.LastName,
		"email":      updatedUser.Email,
	})
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

	var avatarURL string
	cleanPath := strings.TrimSpace(user.ProfileImage)

	if cleanPath != "" {
		cfg := configuration.LoadConfig()
		resService := Resources.NewResourceService(cfg)
		avatarURL, _ = resService.GetAvatarPresignedURL(cleanPath)
	}

	response := map[string]interface{}{
		"id":            user.ID,
		"email":         user.Email,
		"first_name":    user.FirstName,
		"last_name":     user.LastName,
		"profile_image": avatarURL,
		"is_active":     user.IsActive,
		"is_root":       user.IsRoot,
	}

	return utils.Success(c, http.StatusOK, "User profile", response)
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

	cfg := configuration.LoadConfig()
	resService := Resources.NewResourceService(cfg)

	newAvatarPath, err := resService.UploadAvatar(file, header, user.ID)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Error processing image", err.Error())
	}

	if err := userSvc.UpdateProfileImage(user.ID, newAvatarPath); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error saving changes", err.Error())
	}

	signedURL, _ := resService.GetAvatarPresignedURL(newAvatarPath)

	return utils.Success(c, http.StatusOK, "Avatar updated", map[string]string{
		"path": newAvatarPath,
		"url":  signedURL,
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

	userSvc.DeleteUserAvatar(user)

	if err := userSvc.ClearProfileImage(user.ID); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error cleaning avatar", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Avatar deleted", nil)
}

// requireRoot writes an auth/forbidden response and returns false if the requester
// is not a root user. Returns true if the check passed.
func requireRoot(c echo.Context) bool {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		_ = utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Invalid token")
		return false
	}
	requester, err := userSvc.SyncUser(cognitoSub)
	if err != nil || !requester.IsRoot {
		_ = utils.Error(c, http.StatusForbidden, "Forbidden", "Admin only")
		return false
	}
	return true
}

func ListAllUsers(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	page, _ := strconv.Atoi(c.QueryParam("page"))
	pageSize, _ := strconv.Atoi(c.QueryParam("page_size"))
	list, err := adminSvc.ListAllUsers(page, pageSize)
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

	user, err := adminSvc.GetUserDetail(userID)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "User not found", err.Error())
	}

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

	clients, err := adminSvc.ListUserClients(userID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error retrieving user clients", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User clients", clients)
}

func DeactivateUser(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}

	if err := adminSvc.SetUserActive(userID, false); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to deactivate user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User deactivated", nil)
}

func ActivateUser(c echo.Context) error {
	if !requireRoot(c) {
		return nil
	}
	userID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid ID", "")
	}

	if err := adminSvc.SetUserActive(userID, true); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Failed to activate user", err.Error())
	}

	return utils.Success(c, http.StatusOK, "User activated", nil)
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

	return utils.Success(c, http.StatusCreated, "Invitation sent", user)
}
