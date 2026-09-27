package delivery

import (
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

var deliveryClientHealth = map[string]struct{}{"healthy": {}, "watch": {}, "at_risk": {}}

type deliveryClientProfileInput struct {
	Health              string   `json:"health"`
	Contacts            []string `json:"contacts"`
	Rules               []string `json:"rules"`
	ConversationSummary string   `json:"conversation_summary"`
}

type deliveryClientOverview struct {
	Client            models.Client                 `json:"client"`
	Profile           *models.DeliveryClientProfile `json:"profile,omitempty"`
	ProjectCount      int64                         `json:"project_count"`
	ConversationCount int64                         `json:"conversation_count"`
}

// ListClients is the Delivery-specific client directory. It never exposes
// EventiApp operational data; it only lists customers that have an ITBEM
// delivery project and respects project membership for non-admin users.
func ListClients(c echo.Context) error {
	if configuration.DB == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Delivery unavailable", "Database is unavailable")
	}
	user, err := authz.CurrentUser(c)
	if err != nil {
		return deliveryRespondAuthzAndStop(c, err)
	}
	workspaceMode, organizationID, err := deliveryWorkspaceScope(c, user)
	if err != nil {
		return err
	}
	var organizationClientIDs []uuid.UUID
	query := configuration.DB.Model(&models.Client{}).
		Distinct("clients.*").
		Joins("JOIN delivery_projects ON delivery_projects.client_id = clients.id AND delivery_projects.deleted_at IS NULL").
		Preload("DeliveryProfile").
		Order("clients.name ASC")
	if workspaceMode == "organization" {
		clientIDs, err := deliveryOrganizationClientIDs(organizationID)
		if errors.Is(err, errDeliveryOrganizationNotFound) {
			return deliveryResourceNotFound(c)
		}
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Delivery clients unavailable", "Could not load organization clients")
		}
		organizationClientIDs = clientIDs
		query = query.Where("delivery_projects.client_id IN ?", clientIDs)
	}
	if !user.IsPlatformAdmin() {
		query = query.Joins("JOIN delivery_project_members ON delivery_project_members.project_id = delivery_projects.id AND delivery_project_members.cognito_sub = ?", user.CognitoSub)
	}
	var clients []models.Client
	if err := query.Find(&clients).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Delivery clients unavailable", "Could not load delivery clients")
	}
	result := make([]deliveryClientOverview, 0, len(clients))
	for _, client := range clients {
		var projectCount, conversationCount int64
		visibleProjects := deliveryClientVisibleProjectsQuery(user, workspaceMode, organizationClientIDs, client.ID)
		if err := configuration.DB.Model(&models.DeliveryProject{}).
			Where("delivery_projects.client_id = ?", client.ID).
			Where("delivery_projects.id IN (?)", visibleProjects).
			Count(&projectCount).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Delivery clients unavailable", "Could not load project counts")
		}
		if err := configuration.DB.Model(&models.DeliveryContextSource{}).
			Where("delivery_context_sources.kind = ?", "client_conversation").
			Where("delivery_context_sources.project_id IN (?)", visibleProjects).
			Count(&conversationCount).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Delivery clients unavailable", "Could not load conversation counts")
		}
		result = append(result, deliveryClientOverview{Client: client, Profile: client.DeliveryProfile, ProjectCount: projectCount, ConversationCount: conversationCount})
	}
	return utils.Success(c, http.StatusOK, "Delivery clients", result)
}

// deliveryClientVisibleProjectsQuery mirrors the workspace and project-membership
// filters used by ListClients. Client-level aggregate counts must not reveal
// projects or conversation sources that the caller cannot open individually.
func deliveryClientVisibleProjectsQuery(user *models.User, workspaceMode string, organizationClientIDs []uuid.UUID, clientID uuid.UUID) *gorm.DB {
	query := configuration.DB.Model(&models.DeliveryProject{}).
		Select("delivery_projects.id").
		Where("delivery_projects.client_id = ?", clientID)
	if workspaceMode == "organization" {
		query = query.Where("delivery_projects.client_id IN ?", organizationClientIDs)
	}
	if user != nil && !user.IsPlatformAdmin() {
		query = query.Joins("JOIN delivery_project_members ON delivery_project_members.project_id = delivery_projects.id AND delivery_project_members.cognito_sub = ?", user.CognitoSub)
	}
	return query
}

// UpsertClientProfile lets only an ITBEM platform administrator govern the
// reusable client context. Project members can read the profile through the
// directory, but cannot broaden contacts, rules, or health on their own.
func UpsertClientProfile(c echo.Context) error {
	actor, err := admin(c)
	if err != nil {
		return err
	}
	clientID, err := uuid.FromString(strings.TrimSpace(c.Param("id")))
	if err != nil || clientID == uuid.Nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery client", "client id must be a UUID")
	}
	var input deliveryClientProfileInput
	if err := c.Bind(&input); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery client", err.Error())
	}
	health := strings.ToLower(strings.TrimSpace(input.Health))
	if health == "" {
		health = "healthy"
	}
	if _, ok := deliveryClientHealth[health]; !ok || len(input.ConversationSummary) > 12000 {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery client", "health or conversation summary is invalid")
	}
	contacts, rules := sanitizeClientProfileStrings(input.Contacts), sanitizeClientProfileStrings(input.Rules)
	if len(contacts) > 100 || len(rules) > 100 {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery client", "contacts and rules may contain at most 100 entries each")
	}
	contactsJSON, contactsErr := json.Marshal(contacts)
	rulesJSON, rulesErr := json.Marshal(rules)
	if contactsErr != nil || rulesErr != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid delivery client", "contacts or rules are invalid")
	}
	var client models.Client
	if err := configuration.DB.First(&client, clientID).Error; err != nil {
		return lookup(c, "Client", err)
	}
	now := time.Now().UTC()
	profile := models.DeliveryClientProfile{ClientID: clientID}
	err = configuration.DB.Where("client_id = ?", clientID).FirstOrCreate(&profile).Error
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Delivery client unavailable", "Could not load client profile")
	}
	profile.Health, profile.ContactsJSON, profile.RulesJSON = health, string(contactsJSON), string(rulesJSON)
	profile.ConversationSummary, profile.UpdatedBy = sanitizeClientProfileValue(input.ConversationSummary), actor.CognitoSub
	if profile.ConversationSummary != "" {
		profile.LastConversationAt = &now
	}
	if err := configuration.DB.Save(&profile).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Delivery client failed", "Could not save client profile")
	}
	return utils.Success(c, http.StatusOK, "Delivery client profile saved", profile)
}

func sanitizeClientProfileStrings(values []string) []string {
	sanitized := make([]string, 0, len(values))
	for _, value := range values {
		if value = sanitizeClientProfileValue(value); value != "" {
			sanitized = append(sanitized, value)
		}
	}
	return sanitized
}

func sanitizeClientProfileValue(value string) string {
	safe, _ := automationagent.RedactSourceExcerpt(strings.TrimSpace(value))
	return strings.TrimSpace(safe)
}
