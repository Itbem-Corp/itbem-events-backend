package events

import (
	"context"
	"errors"
	"events-stocks/configuration"
	"events-stocks/controllers/publicaccess"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/internal/phrasecatalog"
	"events-stocks/internal/previewtoken"
	"events-stocks/internal/publicaccessproof"
	"events-stocks/internal/tenantresources"
	"events-stocks/models"
	jobqueuerepository "events-stocks/repositories/jobqueuerepository"
	"events-stocks/repositories/phraserepository"
	eventsService "events-stocks/services/events"
	guestsService "events-stocks/services/guests"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var publicPerformanceMetrics = map[string]bool{
	"lcp": true, "inp": true, "cls": true, "page_spec_ms": true,
	"photo_visible_ms": true, "rsvp_submit_ms": true,
}
var publicPerformanceRoutes = map[string]bool{
	"event": true, "moments": true, "rsvp": true, "upload": true, "tv": true,
}

var performanceWindowCleanup struct {
	sync.Mutex
	last time.Time
}

var publicPerformanceBucketBounds = map[string][]float64{
	"cls":              {0.05, 0.1, 0.15, 0.25, 0.4, 0.75, 1, 2, 10, 600000},
	"inp":              {100, 200, 300, 500, 800, 1200, 2500, 10000, 600000},
	"lcp":              {500, 1000, 1800, 2500, 4000, 6000, 10000, 30000, 600000},
	"page_spec_ms":     {100, 250, 500, 800, 1200, 1800, 2500, 4000, 10000, 600000},
	"photo_visible_ms": {250, 500, 1000, 1800, 2500, 4000, 6000, 10000, 30000, 600000},
	"rsvp_submit_ms":   {100, 250, 500, 800, 1200, 1800, 2500, 4000, 10000, 600000},
}

func performanceBucket(metric string, value float64) (int, float64) {
	bounds := publicPerformanceBucketBounds[metric]
	for index, upper := range bounds {
		if value <= upper {
			return index, upper
		}
	}
	return len(bounds) - 1, bounds[len(bounds)-1]
}

var (
	eventSvc             *eventsService.EventService
	eventConfigSvc       *eventsService.EventConfigService
	eventAccessTokenRepo ports.AccessTokenRepository
	eventInvitationRepo  ports.InvitationRepository
	eventGuestSvc        *guestsService.GuestService
)

const eventCoverViewURLTTLMinutes = 720
const eventPasswordAccessTTL = 12 * time.Hour

func InitEventsController(
	svc *eventsService.EventService,
	cfgSvc *eventsService.EventConfigService,
	accessTokenRepo ports.AccessTokenRepository,
	invitationRepo ports.InvitationRepository,
	guestSvc *guestsService.GuestService,
) {
	eventSvc = svc
	eventConfigSvc = cfgSvc
	eventAccessTokenRepo = accessTokenRepo
	eventInvitationRepo = invitationRepo
	eventGuestSvc = guestSvc
}

func coverViewURL(path string) string {
	viewURL, _ := coverViewURLWithExpiry(path)
	return viewURL
}

func coverViewURLWithExpiry(path string, buckets ...string) (string, *time.Time) {
	return resourceViewURLWithExpiry(path, eventCoverViewURLTTLMinutes, buckets...)
}

func resourceViewURLWithExpiry(path string, ttlMinutes int64, buckets ...string) (string, *time.Time) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || utils.IsAbsoluteURLLike(trimmed) || coverResourceSvc == nil {
		return path, nil
	}
	svc := coverResourceSvc
	if len(buckets) > 0 {
		svc = svc.WithBucket(buckets[0])
	}
	viewURL, err := svc.GetPresignedURLWithTTL(trimmed, ttlMinutes)
	if err != nil || viewURL == "" {
		return path, nil
	}
	expiresAt := time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute)
	return viewURL, &expiresAt
}

func eventResponseWithCoverView(event *models.Event) dtos.EventResponse {
	response := dtos.NewEventResponse(event)
	if event == nil {
		return response
	}
	response.CoverViewURL, response.CoverViewURLExpiresAt = coverViewURLWithExpiry(event.CoverImageURL, event.MediaBucket)
	response.CoverViewURL2, response.CoverViewURL2ExpiresAt = coverViewURLWithExpiry(event.CoverImageURL2, event.MediaBucket)
	response.CoverPendingViewURL, response.CoverPendingViewURLExpiresAt = coverViewURLWithExpiry(event.CoverPendingURL, event.MediaBucket)
	response.ViewURL = response.CoverViewURL
	response.ViewURLExpiresAt = response.CoverViewURLExpiresAt
	for i := range response.CoverVariants {
		response.CoverVariants[i].ViewURL, response.CoverVariants[i].ExpiresAt = coverViewURLWithExpiry(response.CoverVariants[i].URL, event.MediaBucket)
	}
	return response
}

func eventResponsesWithCoverView(events []models.Event) []dtos.EventResponse {
	responses := make([]dtos.EventResponse, 0, len(events))
	for i := range events {
		responses = append(responses, eventResponseWithCoverView(&events[i]))
	}
	return responses
}

func withPageSpecCoverViewURL(spec *dtos.PageSpec) *dtos.PageSpec {
	if spec == nil {
		return nil
	}
	response := *spec
	response.Sections = append(make([]dtos.PageSpecSection, 0, len(spec.Sections)), spec.Sections...)
	response.Meta.CoverVariants = append([]dtos.PublicMediaVariant(nil), spec.Meta.CoverVariants...)
	response.Meta.CoverViewURL, response.Meta.CoverViewURLExpiresAt = coverViewURLWithExpiry(spec.Meta.CoverImageURL, spec.MediaBucket)
	for i := range response.Meta.CoverVariants {
		response.Meta.CoverVariants[i].ViewURL, response.Meta.CoverVariants[i].ExpiresAt = coverViewURLWithExpiry(response.Meta.CoverVariants[i].URL, spec.MediaBucket)
	}
	response.Meta.Theme = withPageSpecThemeFontViewURLs(spec.Meta.Theme)
	return &response
}

func pageSpecPublicResponse(c echo.Context, spec *dtos.PageSpec) *dtos.PageSpec {
	if !pageSpecPasswordAccessGranted(c, spec) {
		return lockedPageSpec(spec)
	}
	response := withPageSpecCoverViewURL(spec)
	if response != nil && response.Meta.Access != nil && response.Meta.Access.PasswordProtected {
		response.Meta.Access.PasswordVerified = true
	}
	return response
}

func pageSpecPasswordAccessGranted(c echo.Context, spec *dtos.PageSpec) bool {
	if spec == nil || spec.Meta.Access == nil || !spec.Meta.Access.PasswordProtected {
		return true
	}
	if spec.Meta.Access.PreviewAuthorized {
		return true
	}
	eventID, err := uuid.FromString(spec.Meta.EventID)
	if err != nil {
		return false
	}
	return publicaccessproof.Validate(utils.PublicEventAccessToken(c), eventID, spec.Meta.Access.AccessVersion)
}

func lockedPageSpec(spec *dtos.PageSpec) *dtos.PageSpec {
	if spec == nil {
		return nil
	}
	meta := dtos.PageSpecMeta{
		PageTitle:     spec.Meta.PageTitle,
		EventID:       spec.Meta.EventID,
		Identifier:    spec.Meta.Identifier,
		Timezone:      spec.Meta.Timezone,
		Language:      spec.Meta.Language,
		EventType:     spec.Meta.EventType,
		Access:        clonePageSpecAccess(spec.Meta.Access),
		FooterVisible: false,
	}
	return &dtos.PageSpec{
		Meta:     meta,
		Sections: []dtos.PageSpecSection{},
	}
}

func clonePageSpecAccess(access *dtos.PageSpecAccess) *dtos.PageSpecAccess {
	if access == nil {
		return nil
	}
	clone := *access
	clone.PasswordVerified = false
	return &clone
}

func withPageSpecThemeFontViewURLs(theme *dtos.PageSpecTheme) *dtos.PageSpecTheme {
	if theme == nil || len(theme.FontURLs) == 0 {
		return theme
	}

	response := *theme
	fontViewURLs := make(map[string]string, len(theme.FontURLs))
	var earliestExpiry *time.Time

	for key, rawURL := range theme.FontURLs {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" {
			continue
		}

		viewURL, expiresAt := resourceViewURLWithExpiry(rawURL, resourcesService.ResourceViewURLTTLMinutes)
		viewURL = strings.TrimSpace(viewURL)
		if viewURL == "" {
			continue
		}

		fontViewURLs[cleanKey] = viewURL
		if expiresAt != nil && (earliestExpiry == nil || expiresAt.Before(*earliestExpiry)) {
			expiresAtCopy := *expiresAt
			earliestExpiry = &expiresAtCopy
		}
	}

	if len(fontViewURLs) > 0 {
		response.FontViewURLs = fontViewURLs
		response.FontViewURLsExpiresAt = earliestExpiry
	}
	return &response
}

// GET /api/events  (protected — scoped by client or root)
// ?client_id=UUID  → events for that specific client (access-checked)
// no param + root  → all events
// no param + user  → events for all user's accessible clients
func ListEvents(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	if c.QueryParam("page_size") != "" {
		return listEventPage(c, user)
	}

	clientIDStr := c.QueryParam("client_id")

	if clientIDStr != "" {
		clientID, err := uuid.FromString(clientIDStr)
		if err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid client_id", err.Error())
		}
		if err := authz.RequireClientAccess(user, clientID); err != nil {
			return authz.Respond(c, err)
		}
		events, err := eventSvc.ListEventsByClientID(clientID)
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching events", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Events loaded", eventResponsesWithCoverView(events))
	}

	// No client_id param
	if user.IsPlatformAdmin() {
		events, err := eventSvc.ListEventsForDashboard()
		if err != nil {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching events", err.Error())
		}
		return utils.Success(c, http.StatusOK, "Events loaded", eventResponsesWithCoverView(events))
	}

	// Regular user: return events for all their clients
	events, err := eventSvc.ListEventsForUser(user.ID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching events", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Events loaded", eventResponsesWithCoverView(events))
}

func listEventPage(c echo.Context, user *models.User) error {
	page, err := strconv.Atoi(c.QueryParam("page"))
	if err != nil || page < 1 {
		page = 1
	}
	pageSize, err := strconv.Atoi(c.QueryParam("page_size"))
	if err != nil || pageSize < 1 {
		return utils.Error(c, http.StatusBadRequest, "Invalid page_size", "page_size must be positive")
	}
	if pageSize > 100 {
		pageSize = 100
	}
	filter := strings.ToLower(strings.TrimSpace(c.QueryParam("filter")))
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "upcoming" && filter != "today" && filter != "past" {
		return utils.Error(c, http.StatusBadRequest, "Invalid filter", "filter must be all, upcoming, today, or past")
	}

	var clientID *uuid.UUID
	var userID *uuid.UUID
	if rawClientID := c.QueryParam("client_id"); rawClientID != "" {
		parsedClientID, parseErr := uuid.FromString(rawClientID)
		if parseErr != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid client_id", parseErr.Error())
		}
		if accessErr := authz.RequireClientAccess(user, parsedClientID); accessErr != nil {
			return authz.Respond(c, accessErr)
		}
		clientID = &parsedClientID
	} else if !user.IsPlatformAdmin() {
		userID = &user.ID
	}

	query := dtos.EventListQuery{
		Page:     page,
		PageSize: pageSize,
		Search:   strings.TrimSpace(c.QueryParam("search")),
		Filter:   filter,
		Now:      time.Now(),
	}
	events, total, counts, listErr := eventSvc.ListEventPage(clientID, userID, query)
	if listErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching events", listErr.Error())
	}
	totalPages := (total + pageSize - 1) / pageSize
	return utils.Success(c, http.StatusOK, "Events loaded", dtos.EventListPage{
		Data:       eventResponsesWithCoverView(events),
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
		Counts:     counts,
	})
}

// GET /api/events/dashboard returns the constant-size payload required by the
// operations center. It intentionally avoids generating signed cover URLs for
// an entire portfolio.
func GetDashboardOverview(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}

	var clientID *uuid.UUID
	var userID *uuid.UUID
	clientIDStr := c.QueryParam("client_id")
	if clientIDStr != "" {
		parsedClientID, parseErr := uuid.FromString(clientIDStr)
		if parseErr != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid client_id", parseErr.Error())
		}
		if accessErr := authz.RequireClientAccess(user, parsedClientID); accessErr != nil {
			return authz.Respond(c, accessErr)
		}
		clientID = &parsedClientID
	} else if !user.IsPlatformAdmin() {
		userID = &user.ID
	}

	overview, err := eventSvc.GetDashboardOverview(clientID, userID, time.Now())
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching dashboard events", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Dashboard loaded", overview)
}

func GetEventNotifications(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}
	var clientID, userID *uuid.UUID
	if raw := c.QueryParam("client_id"); raw != "" {
		parsed, parseErr := uuid.FromString(raw)
		if parseErr != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid client_id", parseErr.Error())
		}
		if accessErr := authz.RequireClientAccess(user, parsed); accessErr != nil {
			return authz.Respond(c, accessErr)
		}
		clientID = &parsed
	} else if !user.IsPlatformAdmin() {
		userID = &user.ID
	}
	notifications, listErr := eventSvc.ListEventNotifications(clientID, userID, time.Now())
	if listErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching event notifications", listErr.Error())
	}
	return utils.Success(c, http.StatusOK, "Event notifications loaded", notifications)
}

func GetEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	_, event, authErr := authz.RequireEventAccess(c, id)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}
	if eventConfigSvc == nil || eventGuestSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Event config service unavailable", "")
	}
	var config *models.EventConfig
	var guestSummary dtos.GuestSummary
	var shareSummary dtos.GuestShareSummary
	var sections []models.EventSection
	var configErr, guestSummariesErr, sectionsErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		config, configErr = eventConfigSvc.GetEventConfigByID(id)
	}()
	go func() {
		defer wait.Done()
		guestSummary, shareSummary, guestSummariesErr = eventGuestSvc.GetGuestDashboardSummariesByEventID(id)
	}()
	go func() {
		defer wait.Done()
		sections, sectionsErr = eventsService.ListEventSectionsByEventID(id)
	}()
	wait.Wait()
	if configErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event config", configErr.Error())
	}
	if guestSummariesErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event guest summaries", guestSummariesErr.Error())
	}
	if sectionsErr != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event sections", sectionsErr.Error())
	}
	if config != nil {
		event.EventConfig = *config
	}

	response := eventResponseWithCoverView(event)
	response.GuestSummary = &guestSummary
	response.GuestShareSummary = &shareSummary
	response.EventSections = dtos.NewEventSectionResponses(sections)

	return utils.Success(c, http.StatusOK, "Event loaded", response)
}

// GET /events/:key
func GetEvents(c echo.Context) error {
	keyParam := c.Param("key")
	if keyParam == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing event key", "")
	}
	if keyParam == "all" {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "Use the protected events list endpoint")
	}
	if eventSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Event service unavailable", "")
	}

	event, err := loadPublicEventByKey(keyParam)
	if err != nil || event == nil {
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		return utils.Error(c, http.StatusNotFound, "Event not found", detail)
	}
	allowed, err := allowPublicEventRead(event, utils.PublicPreviewToken(c), utils.PublicInvitationQueryToken(c))
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event config", err.Error())
	}
	if !allowed {
		return utils.Error(c, http.StatusForbidden, "Event is not public", "")
	}

	return utils.Success(c, http.StatusOK, "Event loaded", []dtos.EventResponse{eventResponseWithCoverView(event)})
}

func loadPublicEventByKey(key string) (*models.Event, error) {
	if id, err := uuid.FromString(key); err == nil {
		return eventSvc.GetEventByID(id)
	}
	return eventSvc.GetEventByIdentifier(key)
}

// POST /events
func CreateEvent(c echo.Context) error {
	user, err := authz.CurrentUser(c)
	if err != nil {
		return authz.Respond(c, err)
	}

	var payload dtos.EventPayload
	if err := c.Bind(&payload); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	var event models.Event
	if err := payload.ApplyTo(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(&event); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if err := authz.RequireEventClientForCreate(user, event.ClientID); err != nil {
		return authz.Respond(c, err)
	}
	if _, hasTenant := c.Get("tenant_code").(string); hasTenant {
		event.MediaBucket, err = tenantresources.BucketFromContext(c)
		if err != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Tenant storage is not configured", err.Error())
		}
	}

	if err := eventSvc.CreateEvent(&event); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating event", err.Error())
	}

	return utils.Success(c, http.StatusCreated, "Event created", dtos.NewEventResponse(&event))
}

// PUT /events/:id
func UpdateEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	user, existing, authErr := authz.RequireEventCapability(c, id, authz.CapabilityEventManage)
	if authErr != nil {
		return authz.Respond(c, authErr)
	}

	originalClientID := existing.ClientID
	var payload dtos.EventPayload
	if err := c.Bind(&payload); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := payload.ApplyTo(existing); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid request body", err.Error())
	}
	if err := c.Validate(existing); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Validation error", err.Error())
	}
	if err := authz.RequireClientMoveAccess(user, originalClientID, existing.ClientID); err != nil {
		return authz.Respond(c, err)
	}

	existing.ID = id
	if err := eventSvc.UpdateEvent(existing); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error updating event", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event updated", eventResponseWithCoverView(existing))
}

// DELETE /events/:id
func DeleteEvent(c echo.Context) error {
	idParam := c.Param("id")
	id, err := uuid.FromString(idParam)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}

	if _, _, authErr := authz.RequireEventCapability(c, id, authz.CapabilityEventDelete); authErr != nil {
		return authz.Respond(c, authErr)
	}

	if err := eventSvc.DeleteEvent(id); err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error deleting event", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Event deleted", nil)
}

// GET /events/page-spec?token=...
// Public endpoint — returns the SDUI PageSpec for the event associated with the given invitation token.
func GetPageSpec(c echo.Context) error {
	token := utils.PublicInvitationQueryToken(c)
	if token == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing token parameter", "")
	}

	spec, err := eventsService.GetPageSpecByToken(token)
	if err != nil {
		if errors.Is(err, eventsService.ErrPageSpecTokenExpired) {
			return utils.Error(c, http.StatusUnauthorized, "Invalid invitation token", err.Error())
		}
		if errors.Is(err, eventsService.ErrPageSpecInactive) {
			return utils.Error(c, http.StatusForbidden, "Event is not public", "")
		}
		return utils.Error(c, http.StatusNotFound, "Page spec not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Page spec loaded", pageSpecPublicResponse(c, spec))
}

// GET /api/events/:identifier/page-spec
// Public endpoint — returns the SDUI PageSpec for a public event, admin preview,
// or a private event opened through an invitation token.
func GetPageSpecByIdentifier(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}

	spec, err := eventsService.GetPageSpecByIdentifier(identifier, utils.PublicPreviewToken(c), utils.PublicInvitationQueryToken(c))
	if err != nil {
		if errors.Is(err, eventsService.ErrPageSpecNotPublic) || errors.Is(err, eventsService.ErrPageSpecInactive) {
			return utils.Error(c, http.StatusForbidden, "Event is not public", "")
		}
		return utils.Error(c, http.StatusNotFound, "Page spec not found", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Page spec loaded", pageSpecPublicResponse(c, spec))
}

// POST /api/events/:identifier/view
// Public endpoint — increments the view counter for an event. Fire-and-forget.
// Called by the Astro public page on first load (session-guarded in the client).
func TrackView(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}

	event, err := eventSvc.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		// Return 200 anyway — caller is fire-and-forget, no need to surface 404
		return utils.Success(c, http.StatusOK, "View ignored", dtos.EventViewTrackingResponse{Tracked: false})
	}

	allowed, err := allowPublicViewTracking(event, utils.PublicInvitationQueryToken(c), utils.PublicEventAccessToken(c))
	if err != nil {
		return utils.Success(c, http.StatusOK, "View ignored", dtos.EventViewTrackingResponse{Tracked: false})
	}
	if !allowed {
		return utils.Success(c, http.StatusOK, "View ignored", dtos.EventViewTrackingResponse{Tracked: false})
	}

	// Increment asynchronously — never block the guest page
	go eventsService.IncrementAnalytics(event.ID, "views")

	return utils.Success(c, http.StatusOK, "View tracked", dtos.EventViewTrackingResponse{Tracked: true})
}

// POST /api/events/:identifier/performance
// Accepts aggregate-only RUM samples. Access rules mirror view tracking and no
// request metadata or credential is persisted.
func TrackPerformance(c echo.Context) error {
	identifier := strings.TrimSpace(c.Param("identifier"))
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}
	var body struct {
		Route   string `json:"route"`
		Metrics []struct {
			Name  string  `json:"name"`
			Value float64 `json:"value"`
		} `json:"metrics"`
	}
	if err := c.Bind(&body); err != nil || len(body.Metrics) == 0 || len(body.Metrics) > 12 {
		return utils.Error(c, http.StatusBadRequest, "Invalid performance payload", "")
	}
	body.Route = strings.ToLower(strings.TrimSpace(body.Route))
	if !publicPerformanceRoutes[body.Route] {
		return utils.Error(c, http.StatusBadRequest, "Invalid performance route", "")
	}
	event, err := eventSvc.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return utils.Success(c, http.StatusAccepted, "Performance ignored", map[string]bool{"accepted": false})
	}
	allowed, err := allowPublicViewTracking(event, utils.PublicInvitationQueryToken(c), utils.PublicEventAccessToken(c))
	if err != nil || !allowed {
		return utils.Success(c, http.StatusAccepted, "Performance ignored", map[string]bool{"accepted": false})
	}
	type sample struct {
		name  string
		value float64
	}
	valid := make([]sample, 0, len(body.Metrics))
	for _, metric := range body.Metrics {
		name := strings.ToLower(strings.TrimSpace(metric.Name))
		if !publicPerformanceMetrics[name] || math.IsNaN(metric.Value) || math.IsInf(metric.Value, 0) || metric.Value < 0 || metric.Value > 600000 {
			continue
		}
		valid = append(valid, sample{name: name, value: metric.Value})
	}
	if len(valid) == 0 {
		return utils.Error(c, http.StatusBadRequest, "No valid performance metrics", "")
	}
	go func(eventID uuid.UUID, route string, samples []sample) {
		if configuration.DB == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		bucket := time.Now().UTC().Truncate(24 * time.Hour)
		windowStart := time.Now().UTC().Truncate(5 * time.Minute)
		persisted := false
		for _, metric := range samples {
			row := models.EventPerformanceDaily{EventID: eventID, BucketDate: bucket, Route: route, Metric: metric.name, SampleCount: 1, ValueSum: metric.value, ValueMin: metric.value, ValueMax: metric.value}
			bucketIndex, upperBound := performanceBucket(metric.name, metric.value)
			histogram := models.EventPerformanceBucketDaily{EventID: eventID, BucketDate: bucket, Route: route, Metric: metric.name, BucketIndex: bucketIndex, UpperBound: upperBound, SampleCount: 1}
			window := models.PublicPerformanceWindowBucket{BucketStart: windowStart, Route: route, Metric: metric.name, BucketIndex: bucketIndex, UpperBound: upperBound, SampleCount: 1}
			if err := configuration.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				if createErr := tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "event_id"}, {Name: "bucket_date"}, {Name: "route"}, {Name: "metric"}},
					DoUpdates: clause.Assignments(map[string]interface{}{
						"sample_count": gorm.Expr("event_performance_dailies.sample_count + 1"),
						"value_sum":    gorm.Expr("event_performance_dailies.value_sum + EXCLUDED.value_sum"),
						"value_min":    gorm.Expr("LEAST(event_performance_dailies.value_min, EXCLUDED.value_min)"),
						"value_max":    gorm.Expr("GREATEST(event_performance_dailies.value_max, EXCLUDED.value_max)"),
						"updated_at":   gorm.Expr("NOW()"),
					}),
				}).Create(&row).Error; createErr != nil {
					return createErr
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "event_id"}, {Name: "bucket_date"}, {Name: "route"}, {Name: "metric"}, {Name: "bucket_index"}},
					DoUpdates: clause.Assignments(map[string]interface{}{
						"sample_count": gorm.Expr("event_performance_bucket_dailies.sample_count + 1"),
						"updated_at":   gorm.Expr("NOW()"),
					}),
				}).Create(&histogram).Error; err != nil {
					return err
				}
				return tx.Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "bucket_start"}, {Name: "route"}, {Name: "metric"}, {Name: "bucket_index"}},
					DoUpdates: clause.Assignments(map[string]interface{}{
						"sample_count": gorm.Expr("public_performance_window_buckets.sample_count + 1"),
						"updated_at":   gorm.Expr("NOW()"),
					}),
				}).Create(&window).Error
			}); err != nil {
				slog.Warn("failed to persist public performance aggregate", "event_id", eventID, "route", route, "metric", metric.name, "error", err)
			} else {
				persisted = true
			}
		}
		if persisted {
			if _, err := jobqueuerepository.PublishPerformanceRollup(); err != nil {
				slog.Warn("failed to publish performance rollup", "error", err)
			}
			cleanupPublicPerformanceWindows()
		}
	}(event.ID, body.Route, valid)
	return utils.Success(c, http.StatusAccepted, "Performance accepted", map[string]bool{"accepted": true})
}

func cleanupPublicPerformanceWindows() {
	now := time.Now().UTC()
	performanceWindowCleanup.Lock()
	if now.Sub(performanceWindowCleanup.last) < time.Hour {
		performanceWindowCleanup.Unlock()
		return
	}
	performanceWindowCleanup.last = now
	performanceWindowCleanup.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := configuration.DB.WithContext(ctx).Where("bucket_start < ?", now.Add(-48*time.Hour)).Delete(&models.PublicPerformanceWindowBucket{}).Error; err != nil {
		slog.Warn("failed to prune public performance windows", "error", err)
	}
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
	password := strings.TrimSpace(body.Password)
	if password == "" {
		return utils.Error(c, http.StatusBadRequest, "Password required", "")
	}

	event, err := eventSvc.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return utils.Error(c, http.StatusNotFound, "Event not found", "")
	}

	if eventConfigSvc == nil {
		return utils.Error(c, http.StatusInternalServerError, "Event config service unavailable", "")
	}
	cfg, err := eventConfigSvc.GetEventConfigByID(event.ID)
	if err != nil || cfg == nil {
		return utils.Error(c, http.StatusNotFound, "Event config not found", "")
	}
	allowed, err := allowPublicEventRead(event, utils.PublicPreviewToken(c), utils.PublicInvitationQueryToken(c))
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event config", err.Error())
	}
	if !allowed {
		return utils.Error(c, http.StatusForbidden, "Event is not public", "")
	}

	if !cfg.HasAuthPasswordPreview() {
		// No password set — allow access
		return utils.Success(c, http.StatusOK, "Access granted", dtos.EventAccessVerificationResponse{
			PasswordProtected: false,
		})
	}

	if cfg.NormalizedAuthPasswordPreview() != password {
		return utils.Error(c, http.StatusUnauthorized, "Contraseña incorrecta", "")
	}

	accessVersion := eventsService.EventConfigAccessVersion(cfg)
	accessToken, expiresAt, err := publicaccessproof.Generate(event.ID, accessVersion, eventPasswordAccessTTL)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating access token", err.Error())
	}

	return utils.Success(c, http.StatusOK, "Access granted", dtos.EventAccessVerificationResponse{
		PasswordProtected: true,
		AccessToken:       accessToken,
		AccessTokenType:   "event_password",
		AccessVersion:     accessVersion,
		ExpiresAt:         &expiresAt,
	})
}

func GetEventMeta(c echo.Context) error {
	identifier := c.Param("identifier")
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}
	event, err := eventSvc.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return utils.Error(c, http.StatusNotFound, "Event not found", "")
	}
	allowed, err := allowPublicEventMetaRead(c, event)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error loading event config", err.Error())
	}
	if !allowed {
		return utils.Error(c, http.StatusForbidden, "Event is not public", "")
	}
	eventType := ""
	if event.EventType.Name != "" {
		eventType = event.EventType.Name
	}
	coverURL, coverURLExpiresAt := coverViewURLWithExpiry(event.CoverImageURL, event.MediaBucket)
	var eventDateTime *time.Time
	if !event.EventDateTime.IsZero() {
		eventDateTime = &event.EventDateTime
	}
	contentVersion := ""
	if spec, specErr := eventsService.GetPageSpecByIdentifier(
		identifier,
		utils.PublicPreviewToken(c),
		utils.PublicInvitationQueryToken(c),
	); specErr == nil && spec != nil {
		contentVersion = spec.Meta.ContentVersion
	}
	return utils.Success(c, http.StatusOK, "Event meta loaded", dtos.EventMeta{
		Name:                  event.Name,
		Identifier:            event.Identifier,
		Description:           event.Description,
		CoverImageURL:         event.CoverImageURL,
		CoverViewURL:          coverURL,
		CoverViewURLExpiresAt: coverURLExpiresAt,
		ViewURL:               coverURL,
		ViewURLExpiresAt:      coverURLExpiresAt,
		EventDateTime:         eventDateTime,
		Address:               event.Address,
		SecondAddress:         event.SecondAddress,
		Timezone:              event.Timezone,
		Language:              event.Language,
		OrganizerName:         event.OrganizerName,
		EventType:             eventType,
		ContentVersion:        contentVersion,
	})
}

func allowPublicEventRead(event *models.Event, previewToken, invitationToken string) (bool, error) {
	if event == nil {
		return false, nil
	}
	return publicaccess.AllowEventRead(event.ID, previewToken, invitationToken, eventPublicReadDeps(func(uuid.UUID) (bool, error) {
		return event.IsActive, nil
	}))
}

func allowPublicEventMetaRead(c echo.Context, event *models.Event) (bool, error) {
	if event == nil {
		return false, nil
	}
	deps := eventPublicReadDeps(func(uuid.UUID) (bool, error) {
		return event.IsActive, nil
	})
	deps.RequirePasswordProof = true
	return publicaccess.AllowEventReadFromRequest(c, event.ID, deps)
}

func allowPublicViewTracking(event *models.Event, invitationToken, accessToken string) (bool, error) {
	if event == nil {
		return false, nil
	}
	deps := eventPublicReadDeps(func(uuid.UUID) (bool, error) {
		return event.IsActive, nil
	})
	deps.RequirePasswordProof = true
	deps.PasswordProofToken = accessToken
	return publicaccess.AllowEventRead(event.ID, "", invitationToken, deps)
}

func eventPublicReadDeps(isEventActive func(uuid.UUID) (bool, error)) publicaccess.EventReadDeps {
	var cfgRepo ports.EventConfigRepository
	if eventConfigSvc != nil {
		cfgRepo = eventConfigSvc
	}
	return publicaccess.EventReadDeps{
		ConfigRepo:     cfgRepo,
		TokenRepo:      eventAccessTokenRepo,
		InvitationRepo: eventInvitationRepo,
		IsEventActive:  isEventActive,
	}
}

func IssuePreviewToken(c echo.Context) error {
	eventID, err := uuid.FromString(c.Param("id"))
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid UUID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityEventManage); authErr != nil {
		return authz.Respond(c, authErr)
	}
	token, expiresAt, err := previewtoken.GenerateWithExpiry(eventID, 30*time.Minute)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error creating preview token", err.Error())
	}
	return utils.Success(c, http.StatusOK, "Preview token created", dtos.PreviewTokenResponse{Token: token, ExpiresAt: expiresAt})
}

func ListPhrases(c echo.Context) error {
	phraseType := c.QueryParam("type")
	if phraseType == "" {
		phraseType = "general"
	}
	count := 15
	if rawCount := c.QueryParam("count"); rawCount != "" {
		if parsed, err := strconv.Atoi(rawCount); err == nil && parsed > 0 {
			count = parsed
		}
	}
	if count > 50 {
		count = 50
	}
	source := phrasesByType(phraseType)
	if stored, err := phraserepository.ListByEventType(c.Request().Context(), storedPhraseType(phraseType)); err == nil && len(stored) > 0 {
		source = stored
	}
	phrases := selectPhrases(source, count)
	c.Response().Header().Set(echo.HeaderCacheControl, "public, max-age=3600, stale-while-revalidate=86400")
	return utils.Success(c, http.StatusOK, "Phrases loaded", dtos.EventPhrasesResponse{Phrases: phrases})
}

func phrasesByType(phraseType string) []string {
	return phrasecatalog.ForType(normalizePhraseType(phraseType))
}

func storedPhraseType(phraseType string) string {
	switch normalizePhraseType(phraseType) {
	case "wedding", "boda":
		return "WEDDING"
	case "graduation", "graduacion":
		return "GRADUATION"
	default:
		return "DEFAULT"
	}
}

func selectPhrases(source []string, count int) []string {
	if count <= 0 || len(source) == 0 {
		return []string{}
	}
	if count > len(source) {
		count = len(source)
	}
	shuffled := append([]string(nil), source...)
	random := rand.New(rand.NewSource(time.Now().UnixNano()))
	random.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled[:count]
}

func normalizePhraseType(phraseType string) string {
	normalized := strings.ToLower(strings.TrimSpace(phraseType))
	replacer := strings.NewReplacer(
		"á", "a",
		"é", "e",
		"í", "i",
		"ó", "o",
		"ú", "u",
		"ñ", "n",
	)
	return replacer.Replace(normalized)
}
