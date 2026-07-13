package eventconfig

import (
	"encoding/json"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	resourcesService "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
	"net/http"
	"strings"
	"time"
)

var eventConfigSvc *eventsService.EventConfigService
var eventConfigResourceSvc *resourcesService.ResourceService

func InitEventConfigController(svc *eventsService.EventConfigService, resourceSvc ...*resourcesService.ResourceService) {
	eventConfigSvc = svc
	eventConfigResourceSvc = nil
	if len(resourceSvc) > 0 {
		eventConfigResourceSvc = resourceSvc[0]
	}
}

func eventConfigCatalogViewURLWithExpiry(path string) (string, *time.Time) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || utils.IsAbsoluteURLLike(trimmed) || eventConfigResourceSvc == nil {
		return path, nil
	}

	viewURL, err := eventConfigResourceSvc.GetPresignedURLWithTTL(trimmed, resourcesService.ResourceViewURLTTLMinutes)
	if err != nil || strings.TrimSpace(viewURL) == "" {
		return path, nil
	}

	expiresAt := time.Now().UTC().Add(time.Duration(resourcesService.ResourceViewURLTTLMinutes) * time.Minute)
	return viewURL, &expiresAt
}

func eventConfigFontResponseWithViewURL(font *dtos.FontResponse) {
	if font == nil {
		return
	}
	font.ViewURL, font.ViewURLExpiresAt = eventConfigCatalogViewURLWithExpiry(font.URL)
}

func eventConfigFontSetResponseWithViewURLs(fontSet *dtos.FontSetResponse) {
	if fontSet == nil {
		return
	}
	for i := range fontSet.Patterns {
		eventConfigFontResponseWithViewURL(fontSet.Patterns[i].Font)
	}
}

func eventConfigDesignTemplateResponseWithViewURLs(template *dtos.DesignTemplateResponse) {
	if template == nil {
		return
	}
	previewPath := template.PreviewURL
	if strings.TrimSpace(previewPath) == "" {
		previewPath = template.PreviewImageURL
	}
	template.PreviewViewURL, template.PreviewViewURLExpiresAt = eventConfigCatalogViewURLWithExpiry(previewPath)
	eventConfigFontSetResponseWithViewURLs(template.FontSet)
	if template.DefaultFontSet != template.FontSet {
		eventConfigFontSetResponseWithViewURLs(template.DefaultFontSet)
	}
}

func eventConfigResponse(config *models.EventConfig, eventID uuid.UUID) dtos.EventConfigResponse {
	response := dtos.NewEventConfigResponse(config, eventID)
	eventConfigDesignTemplateResponseWithViewURLs(response.DesignTemplate)
	eventConfigFontSetResponseWithViewURLs(response.FontSet)
	return response
}

// GetStudioWorkspace composes the resources required for Studio's first
// interactive paint while preserving their independent mutation endpoints.
func GetStudioWorkspace(c echo.Context) error {
	id, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	_, event, authErr := authz.RequireEventAccess(c, id)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	var config *models.EventConfig
	var sections []models.EventSection
	group := new(errgroup.Group)
	group.Go(func() error {
		loaded, loadErr := eventConfigSvc.GetEventConfigByID(id)
		config = loaded
		return loadErr
	})
	group.Go(func() error {
		loaded, loadErr := eventsService.ListEventSectionsByEventID(id)
		sections = loaded
		return loadErr
	})
	if err := group.Wait(); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading Studio workspace", err.Error())
	}

	response := dtos.StudioWorkspaceResponse{
		Event:    dtos.NewEventResponse(event),
		Config:   eventConfigResponse(config, id),
		Sections: dtos.NewEventSectionResponses(sections),
	}
	return utils.Success(c, http.StatusOK, "Studio workspace loaded", response)
}

// GET /events/:id/config
func GetEventConfig(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, id, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	config, err := eventConfigSvc.GetEventConfigByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event config loaded", eventConfigResponse(config, id))
}

// PUT /events/:id/config
func UpdateEventConfig(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, id, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}

	config, err := eventConfigSvc.GetEventConfigByID(id)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", err.Error())
	}

	var patch dtos.EventConfigPatch
	if err := json.NewDecoder(c.Request().Body).Decode(&patch); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if patch == nil {
		patch = dtos.EventConfigPatch{}
	}

	if err := patch.ApplyTo(config); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event config field", err.Error())
	}
	config.ID = id
	if err := eventConfigSvc.UpdateEventConfig(config); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating event config", err.Error())
	}

	updatedConfig, err := eventConfigSvc.GetEventConfigByID(id)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading updated event config", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event config updated", eventConfigResponse(updatedConfig, id))
}
