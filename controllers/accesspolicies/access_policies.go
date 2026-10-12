package accesspolicies

import (
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/capabilities"
	"events-stocks/internal/products"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"net/http"
	"sort"
	"strings"
)

func Manage(c echo.Context) error {
	actor, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if !actor.IsPrimaryRoot() {
		return utils.Error(c, http.StatusForbidden, "Forbidden", "only a primary root can manage global access policies")
	}
	userID, err := uuid.FromString(c.Param("user_id"))
	if err != nil {
		return utils.Error(c, 400, "Invalid user", "user_id must be a UUID")
	}
	var target models.User
	if err := configuration.DB.Where("id = ?", userID).First(&target).Error; err != nil {
		return utils.Error(c, http.StatusNotFound, "User not found", "")
	}
	if target.IsPrimaryRoot() {
		return utils.Error(c, http.StatusConflict, "Protected account", "primary roots retain their protected global authority")
	}
	definition, ok := products.Resolve(c.Param("application_code"))
	if !ok {
		return utils.Error(c, 404, "Application not found", "")
	}
	var application models.Application
	if err := configuration.DB.Where("code = ?", definition.Code.String()).First(&application).Error; err != nil {
		return utils.Error(c, 404, "Application not found", "")
	}
	if c.Request().Method == http.MethodGet {
		var policy models.UserApplicationPolicy
		result := configuration.DB.Where("user_id = ? AND application_id = ?", userID, application.ID).First(&policy)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return utils.Success(c, 200, "Inherited access", map[string]any{"mode": "inherited", "application_code": application.Code, "is_active": true, "capabilities": []string{}})
		}
		if result.Error != nil {
			return utils.Error(c, http.StatusInternalServerError, "Could not load access policy", "")
		}
		return utils.Success(c, 200, "Access policy", map[string]any{"mode": "explicit", "application_code": application.Code, "is_active": policy.IsActive, "capabilities": policy.Capabilities})
	}
	var request struct {
		IsActive     bool     `json:"is_active"`
		Capabilities []string `json:"capabilities"`
	}
	if err := c.Bind(&request); err != nil {
		return utils.Error(c, 400, "Invalid access policy", err.Error())
	}
	values := make([]string, 0, len(request.Capabilities))
	seen := map[string]struct{}{}
	for _, value := range request.Capabilities {
		value = strings.ToLower(strings.TrimSpace(value))
		// The allow-list is the single table in internal/capabilities; it is
		// also the set of names the server-side policy ceiling governs.
		if !capabilities.PolicyAllows(application.Code, value) {
			return utils.Error(c, 400, "Invalid capability", value)
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			values = append(values, value)
		}
	}
	sort.Strings(values)
	policy := models.UserApplicationPolicy{UserID: userID, ApplicationID: application.ID, IsActive: request.IsActive, Capabilities: models.StringList(values), CreatedByUserID: &actor.ID}
	if err := configuration.DB.Where("user_id = ? AND application_id = ?", userID, application.ID).Assign(policy).FirstOrCreate(&policy).Error; err != nil {
		return utils.Error(c, 500, "Could not save access policy", "")
	}
	return utils.Success(c, 200, "Access policy saved", map[string]any{"mode": "explicit", "application_code": application.Code, "is_active": policy.IsActive, "capabilities": policy.Capabilities})
}
