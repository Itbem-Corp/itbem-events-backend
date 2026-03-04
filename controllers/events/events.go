package events

import (
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/clientrepository"
	"events-stocks/repositories/eventconfigrepository"
	"events-stocks/repositories/eventsrepository"
	eventsService "events-stocks/services/events"
	"events-stocks/services/users"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"net/http"
)

var eventSvc *eventsService.EventService

func InitEventsController(svc *eventsService.EventService) {
	eventSvc = svc
}

// GET /api/events  (protected — scoped by client or root)
// ?client_id=UUID  → events for that specific client (access-checked)
// no param + root  → all events
// no param + user  → events for all user's accessible clients
func ListEvents(c echo.Context) error {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	user, err := users.SyncUser(cognitoSub)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "User not found", err.Error())
	}

	// Extract config once — used to resolve cover image presigned URLs.
	cfg, cfgOK := c.Get("config").(*models.Config)

	clientIDStr := c.QueryParam("client_id")

	if clientIDStr != "" {
		clientID, err := uuid.FromString(clientIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid client_id", err.Error())
		}
		// Root users can access any client; regular users need to have access
		if !user.IsRoot {
			allowed, _ := clientrepository.CheckAccessRecursive(user.ID, clientID)
			if !allowed {
				return utils.Error(c, http.StatusForbidden, "Access denied", "You don't have access to this client's events")
			}
		}
		events, err := eventsrepository.GetEventsByClientID(clientID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching events", err.Error())
		}
		if cfgOK && cfg != nil {
			for i := range events {
				events[i].CoverImageURL = resolveCoverURL(events[i].CoverImageURL, cfg.AwsBucketName)
				events[i].CoverImageURL2 = resolveCoverURL(events[i].CoverImageURL2, cfg.AwsBucketName)
			}
		}
		return utils.Success(c, http.StatusOK, "Events loaded", events)
	}

	// No client_id param
	if user.IsRoot {
		events, err := eventsrepository.GetAllEventsForDashboard()
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching events", err.Error())
		}
		if cfgOK && cfg != nil {
			for i := range events {
				events[i].CoverImageURL = resolveCoverURL(events[i].CoverImageURL, cfg.AwsBucketName)
				events[i].CoverImageURL2 = resolveCoverURL(events[i].CoverImageURL2, cfg.AwsBucketName)
			}
		}
		return utils.Success(c, http.StatusOK, "Events loaded", events)
	}

	// Regular user: return events for all their clients
	events, err := eventsrepository.GetEventsForUser(user.ID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching events", err.Error())
	}
	if cfgOK && cfg != nil {
		for i := range events {
			events[i].CoverImageURL = resolveCoverURL(events[i].CoverImageURL, cfg.AwsBucketName)
			events[i].CoverImageURL2 = resolveCoverURL(events[i].CoverImageURL2, cfg.AwsBucketName)
		}
	}
	return utils.Success(c, http.StatusOK, "Events loaded", events)
}

// GET /events/:key
func GetEvents(c echo.Context) error {
	keyParam := c.Param("key")
	redisKey := keyParam + ":" + utils.RedisServiceEventsKey

	dataStr, ok := c.Get(redisKey).(string)
	if !ok {
		return utils.Success(c, http.StatusOK, "No data loaded", nil)
	}

	if keyParam == "all" {
		var events []models.Event
		if err := json.Unmarshal([]byte(dataStr), &events); err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Events loaded", events)
	}

	var event []models.Event
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error parsing data", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Event loaded", event)
}

// POST /events
func CreateEvent(c echo.Context) error {
	var event models.Event
	if err := c.Bind(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	if err := eventSvc.CreateEvent(&event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating event", err.Error())
	}

	cfg := c.Get("config").(*models.Config)
	event.CoverImageURL = resolveCoverURL(event.CoverImageURL, cfg.AwsBucketName)
	event.CoverImageURL2 = resolveCoverURL(event.CoverImageURL2, cfg.AwsBucketName)

	return utils.Success(c, http.StatusCreated, "Event created", event)
}

// PUT /events/:id
func UpdateEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	var event models.Event
	if err := c.Bind(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}

	event.ID = id
	if err := eventSvc.UpdateEvent(&event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating event", err.Error())
	}

	cfg := c.Get("config").(*models.Config)
	event.CoverImageURL = resolveCoverURL(event.CoverImageURL, cfg.AwsBucketName)
	event.CoverImageURL2 = resolveCoverURL(event.CoverImageURL2, cfg.AwsBucketName)

	return utils.Success(c, http.StatusOK, "Event updated", event)
}

// DELETE /events/:id
func DeleteEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if err := eventSvc.DeleteEvent(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting event", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event deleted", nil)
}

// GET /events/page-spec?token=...
// Public endpoint — returns the SDUI PageSpec for the event associated with the given invitation token.
func GetPageSpec(c echo.Context) error {
	token := c.QueryParam("token")
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing token parameter", "")
	}

	spec, err := eventsService.GetPageSpecByToken(token)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Page spec not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Page spec loaded", spec)
}

// GET /api/events/:identifier/page-spec
// Public endpoint — returns the SDUI PageSpec for a public preview of the event.
// Used by the Astro /e/{identifier} route (no invitation token required).
func GetPageSpecByIdentifier(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}

	spec, err := eventsService.GetPageSpecByIdentifier(identifier)
	if err != nil {
		return utils.Error(c, http.StatusNotFound, "Page spec not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Page spec loaded", spec)
}

// POST /api/events/:identifier/view
// Public endpoint — increments the view counter for an event. Fire-and-forget.
// Called by the Astro public page on first load (session-guarded in the client).
func TrackView(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}

	event, err := eventsrepository.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		// Return 200 anyway — caller is fire-and-forget, no need to surface 404
		return utils.Success(c, http.StatusOK, "ok", nil)
	}

	// Increment asynchronously — never block the guest page
	go eventsService.IncrementAnalytics(event.ID, "views")

	return utils.Success(c, http.StatusOK, "ok", nil)
}

// POST /api/events/:identifier/verify-access
// Public endpoint — verifies the password for a password-protected event.
// Returns 200 if correct, 401 if wrong. The password itself is never exposed.
func VerifyEventAccess(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}

	var body struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&body); err != nil || body.Password == "" {
		return utils.Error(c, http.StatusBadRequest, "Password required", "")
	}

	event, err := eventsrepository.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return utils.Error(c, http.StatusNotFound, "Event not found", "")
	}

	cfg, err := eventconfigrepository.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}

	if cfg.AuthPasswordPreview == "" {
		// No password set — allow access
		return utils.Success(c, http.StatusOK, "Access granted", nil)
	}

	if cfg.AuthPasswordPreview != body.Password {
		return utils.Error(c, http.StatusUnauthorized, "Contraseña incorrecta", "")
	}

	return utils.Success(c, http.StatusOK, "Access granted", nil)
}

