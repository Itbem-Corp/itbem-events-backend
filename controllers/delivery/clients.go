package delivery

import (
	"encoding/json"
	"events-stocks/configuration"
	"events-stocks/internal/authz"
	"events-stocks/models"
	"events-stocks/utils"
	"net/http"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
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
		return authz.Respond(c, err)
	}
	query := configuration.DB.Model(&models.Client{}).
		Distinct("clients.*").
		Joins("JOIN delivery_projects ON delivery_projects.client_id = clients.id AND delivery_projects.deleted_at IS NULL").
		Preload("DeliveryProfile").
		Order("clients.name ASC")
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
		if err := configuration.DB.Model(&models.DeliveryProject{}).Where("client_id = ?", client.ID).Count(&projectCount).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Delivery clients unavailable", "Could not load project counts")
		}
		if err := configuration.DB.Model(&models.DeliveryContextSource{}).
			Joins("JOIN delivery_projects ON delivery_projects.id = delivery_context_sources.project_id").
			Where("delivery_projects.client_id = ? AND delivery_context_sources.kind = ?", client.ID, "client_conversation").
			Count(&conversationCount).Error; err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Delivery clients unavailable", "Could not load conversation counts")
		}
		result = append(result, deliveryClientOverview{Client: client, Profile: client.DeliveryProfile, ProjectCount: projectCount, ConversationCount: conversationCount})
	}
	return utils.Success(c, http.StatusOK, "Delivery clients", result)
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
	contacts, rules := cleanStrings(input.Contacts), cleanStrings(input.Rules)
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
	profile.ConversationSummary, profile.UpdatedBy = strings.TrimSpace(input.ConversationSummary), actor.CognitoSub
	if profile.ConversationSummary != "" {
		profile.LastConversationAt = &now
	}
	if err := configuration.DB.Save(&profile).Error; err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Delivery client failed", "Could not save client profile")
	}
	return utils.Success(c, http.StatusOK, "Delivery client profile saved", profile)
}
