package events

import (
	"net/http"

	"events-stocks/models"
	"events-stocks/repositories/eventtyperepository"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/utils"
	"github.com/labstack/echo/v4"
)

var metaCfg *models.Config

// InitMetaController stores the app config for use by GetEventMeta.
// Called once from server.go at startup.
func InitMetaController(cfg *models.Config) {
	metaCfg = cfg
}

// GetEventMeta handles GET /api/events/:identifier/meta
// Public endpoint — returns minimal event data for OG meta tags.
func GetEventMeta(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}

	event, err := eventsrepository.GetEventByIdentifier(identifier)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Event not found", "")
	}

	// Resolve cover URL from S3 path to presigned HTTPS URL.
	bucket := ""
	if metaCfg != nil {
		bucket = metaCfg.AwsBucketName
	}
	coverURL := resolveCoverURL(event.CoverImageURL, bucket)

	// Look up event type name (GetEventByIdentifier does not preload relations).
	eventTypeName := ""
	eventType, err := eventtyperepository.GetEventTypeByID(event.EventTypeID)
	if err == nil && eventType != nil {
		eventTypeName = eventType.Name
	}

	c.Response().Header().Set("Cache-Control", "public, max-age=3600")

	return utils.Success(c, http.StatusOK, "Event meta loaded", map[string]interface{}{
		"name":            event.Name,
		"cover_image_url": coverURL,
		"event_type":      eventTypeName,
		"event_date_time": event.EventDateTime,
	})
}
