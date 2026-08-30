package events

import (
	"context"
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/storagekeys"
	"events-stocks/models"
	sqsrepository "events-stocks/repositories/sqsrepository"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrStaleCoverProcessing             = ports.ErrStaleCoverProcessing
	ErrInvalidCoverProcessingTransition = ports.ErrInvalidCoverProcessingTransition
)

var ErrEventNotFound = errors.New("event not found")

// _eventSvc is the package-level singleton set by internal/app.
var _eventSvc *EventService

// SetDefaultEventService wires the package-level functions to the DI instance.
func SetDefaultEventService(svc *EventService) { _eventSvc = svc }

func ListEvents() ([]models.Event, error)              { return _eventSvc.ListEvents() }
func GetEventByID(id uuid.UUID) (*models.Event, error) { return _eventSvc.GetEventByID(id) }
func CreateEvent(obj *models.Event) error              { return _eventSvc.CreateEvent(obj) }
func UpdateEvent(obj *models.Event) error              { return _eventSvc.UpdateEvent(obj) }
func DeleteEvent(id uuid.UUID) error                   { return _eventSvc.DeleteEvent(id) }
func GetEventByIdentifier(identifier string) (*models.Event, error) {
	return _eventSvc.GetEventByIdentifier(identifier)
}

// EventService is the injectable, struct-based events service.
type EventService struct {
	repo  ports.EventsRepository
	cache ports.CacheRepository
}

func NewEventService(repo ports.EventsRepository, cache ports.CacheRepository) *EventService {
	return &EventService{repo: repo, cache: cache}
}

func (s *EventService) ListEvents() ([]models.Event, error) {
	cacheKey := "all:" + utils.RedisServiceEventsKey
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		cacheKey,
		utils.CacheTTLs[utils.RedisServiceEventsKey],
		func() ([]models.Event, error) {
			return s.repo.ListEvents(1, 0, "")
		},
	)
}

func (s *EventService) GetEventByID(id uuid.UUID) (*models.Event, error) {
	jsonStr, err := s.repo.GetEventByID(id)
	if err != nil {
		return nil, err
	}
	// The repo wraps the result in an array for cache-loader compatibility,
	// so we must unmarshal as a slice and return the first element.
	var events []models.Event
	if err := json.Unmarshal([]byte(jsonStr), &events); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("event %s not found", id)
	}
	return &events[0], nil
}

func (s *EventService) GetEventByIdentifier(identifier string) (*models.Event, error) {
	return s.repo.GetEventByIdentifier(identifier)
}

func (s *EventService) ListEventsByClientID(clientID uuid.UUID) ([]models.Event, error) {
	return s.repo.GetEventsByClientID(clientID)
}

func (s *EventService) ListEventsForDashboard() ([]models.Event, error) {
	return s.repo.GetAllEventsForDashboard()
}

func (s *EventService) ListEventsForUser(userID uuid.UUID) ([]models.Event, error) {
	return s.repo.GetEventsForUser(userID)
}

func (s *EventService) GetDashboardOverview(clientID, userID *uuid.UUID, now time.Time) (dtos.EventDashboardOverview, error) {
	if optimized, ok := s.repo.(ports.EventsDashboardRepository); ok {
		return optimized.GetEventDashboardOverview(clientID, userID, now)
	}

	var (
		events []models.Event
		err    error
	)
	if clientID != nil {
		events, err = s.repo.GetEventsByClientID(*clientID)
	} else if userID != nil {
		events, err = s.repo.GetEventsForUser(*userID)
	} else {
		events, err = s.repo.GetAllEventsForDashboard()
	}
	if err != nil {
		return dtos.EventDashboardOverview{}, err
	}
	return BuildDashboardOverview(events, now), nil
}

func (s *EventService) ListEventPage(clientID, userID *uuid.UUID, query dtos.EventListQuery) ([]models.Event, int, dtos.EventListCounts, error) {
	if optimized, ok := s.repo.(ports.EventsPageRepository); ok {
		return optimized.ListEventPage(clientID, userID, query)
	}

	var (
		events []models.Event
		err    error
	)
	if clientID != nil {
		events, err = s.repo.GetEventsByClientID(*clientID)
	} else if userID != nil {
		events, err = s.repo.GetEventsForUser(*userID)
	} else {
		events, err = s.repo.GetAllEventsForDashboard()
	}
	if err != nil {
		return nil, 0, dtos.EventListCounts{}, err
	}

	counts := dtos.EventListCounts{All: len(events)}
	filtered := make([]models.Event, 0, len(events))
	search := strings.ToLower(strings.TrimSpace(query.Search))
	for i := range events {
		event := events[i]
		days := dashboardCalendarDaysUntil(event.EventDateTime, event.Timezone, query.Now)
		switch {
		case days > 0:
			counts.Upcoming++
		case days < 0:
			counts.Past++
		default:
			counts.Today++
		}
		matchesSearch := search == "" || strings.Contains(strings.ToLower(event.Name), search) ||
			strings.Contains(strings.ToLower(event.Address), search) ||
			strings.Contains(strings.ToLower(event.OrganizerName), search)
		matchesFilter := query.Filter == "all" ||
			(query.Filter == "upcoming" && days > 0) ||
			(query.Filter == "today" && days == 0) ||
			(query.Filter == "past" && days < 0)
		if matchesSearch && matchesFilter {
			filtered = append(filtered, event)
		}
	}

	total := len(filtered)
	start := (query.Page - 1) * query.PageSize
	if start >= total {
		return []models.Event{}, total, counts, nil
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	return filtered[start:end], total, counts, nil
}

func (s *EventService) ListEventNotifications(clientID, userID *uuid.UUID, now time.Time) ([]dtos.EventNotification, error) {
	if optimized, ok := s.repo.(ports.EventsNotificationRepository); ok {
		return optimized.ListEventNotifications(clientID, userID, now)
	}
	var source []models.Event
	var err error
	if clientID != nil {
		source, err = s.repo.GetEventsByClientID(*clientID)
	} else if userID != nil {
		source, err = s.repo.GetEventsForUser(*userID)
	} else {
		source, err = s.repo.GetAllEventsForDashboard()
	}
	if err != nil {
		return nil, err
	}
	result := make([]dtos.EventNotification, 0)
	for i := range source {
		event := source[i]
		if days := dashboardCalendarDaysUntil(event.EventDateTime, event.Timezone, now); days >= -3 && days <= 3 {
			result = append(result, dtos.EventNotification{ID: event.ID, Name: event.Name, Identifier: event.Identifier, EventDateTime: event.EventDateTime, Timezone: event.Timezone, IsActive: event.IsActive, ClientID: event.ClientID, EventTypeID: event.EventTypeID})
		}
	}
	return result, nil
}

func BuildDashboardOverview(events []models.Event, now time.Time) dtos.EventDashboardOverview {
	overview := dtos.EventDashboardOverview{ActiveEvents: make([]dtos.EventResponse, 0, 5)}
	var nextEvent *models.Event

	for i := range events {
		event := &events[i]
		overview.Metrics.Total++
		if event.MaxGuests != nil {
			overview.Metrics.TotalCapacity += *event.MaxGuests
		}
		if !event.IsActive {
			continue
		}
		overview.Metrics.Active++
		if dashboardCalendarDaysUntil(event.EventDateTime, event.Timezone, now) >= 0 {
			overview.Metrics.Upcoming++
			if nextEvent == nil || event.EventDateTime.Before(nextEvent.EventDateTime) {
				nextEvent = event
			}
		} else {
			overview.Metrics.PastActive++
		}
		if len(overview.ActiveEvents) < 5 {
			overview.ActiveEvents = append(overview.ActiveEvents, dtos.NewEventResponse(event))
		}
	}
	if nextEvent != nil {
		response := dtos.NewEventResponse(nextEvent)
		overview.NextEvent = &response
	}
	return overview
}

func dashboardCalendarDaysUntil(eventTime time.Time, timeZone string, now time.Time) int {
	location := time.UTC
	if loaded, err := time.LoadLocation(strings.TrimSpace(timeZone)); err == nil {
		location = loaded
	}
	eventDate := eventTime.In(location)
	nowDate := now.In(location)
	eventDay := time.Date(eventDate.Year(), eventDate.Month(), eventDate.Day(), 0, 0, 0, 0, time.UTC)
	today := time.Date(nowDate.Year(), nowDate.Month(), nowDate.Day(), 0, 0, 0, 0, time.UTC)
	return int(eventDay.Sub(today) / (24 * time.Hour))
}

func (s *EventService) CreateEvent(obj *models.Event) error {
	if obj.Identifier == "" && obj.Name != "" {
		obj.Identifier = s.generateUniqueIdentifier(obj.Name)
	}
	if err := s.repo.CreateEvent(obj); err != nil {
		return err
	}
	// Auto-create related records so dashboard doesn't get 404s.
	// Best-effort: config/analytics endpoints also auto-create on first access.
	s.autoCreateRelated(obj.ID)
	return invalidateEventsCache(s.cache)
}

// generateUniqueIdentifier creates a unique slug from the event name.
func (s *EventService) generateUniqueIdentifier(name string) string {
	base := utils.Slugify(name)
	if base == "" {
		base = "event"
	}
	candidate := base
	for i := 2; s.repo.IdentifierExists(candidate); i++ {
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return candidate
}

// autoCreateRelated creates EventConfig and EventAnalytics for a new event.
// Failures are silently ignored — both endpoints auto-create on first access.
func (s *EventService) autoCreateRelated(eventID uuid.UUID) {
	defer func() { recover() }()
	if _eventConfigSvc != nil {
		_ = CreateEventConfig(NewDefaultEventConfig(eventID))
	}
	_ = CreateEventAnalytics(&models.EventAnalytics{EventID: eventID})
}

func (s *EventService) UpdateEvent(obj *models.Event) error {
	if err := s.repo.UpdateEvent(obj); err != nil {
		return err
	}
	return invalidateEventsCache(s.cache)
}

func (s *EventService) ReplaceCoverImage(eventID uuid.UUID, s3Path string) (string, error) {
	oldPath, _, err := s.ReplaceCoverImageWithVariants(eventID, s3Path, models.MediaVariants{})
	return oldPath, err
}

func (s *EventService) ReplaceCoverImageWithVariants(eventID uuid.UUID, s3Path string, variants models.MediaVariants) (string, models.MediaVariants, error) {
	event, err := s.repo.GetEventByIDRaw(eventID)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrEventNotFound, err)
	}
	oldPath := event.CoverImageURL
	oldVariants := event.CoverVariants
	if repo, ok := s.repo.(ports.EventCoverVariantsRepository); ok {
		err = repo.UpdateEventCoverWithVariants(eventID, s3Path, variants)
	} else {
		err = s.repo.UpdateEventCover(eventID, s3Path)
	}
	if err != nil {
		return "", nil, err
	}
	_ = invalidateEventsCache(s.cache)
	return oldPath, oldVariants, nil
}

func (s *EventService) BeginCoverProcessing(eventID uuid.UUID, pendingURL, jobID string) (*models.Event, string, error) {
	repo, ok := s.repo.(ports.EventCoverProcessingRepository)
	if !ok {
		return nil, "", fmt.Errorf("event cover processing repository is not configured")
	}
	if strings.TrimSpace(pendingURL) == "" || strings.TrimSpace(jobID) == "" {
		return nil, "", fmt.Errorf("pending cover URL and job ID are required")
	}
	event, previousPending, err := repo.BeginEventCoverProcessing(eventID, pendingURL, jobID)
	if err != nil {
		return nil, "", err
	}
	_ = invalidateEventsCache(s.cache)
	return event, previousPending, nil
}

// BeginCoverProcessingWithOutbox creates a durable Lambda handoff whenever
// the production repository and image queue are available. The bool reports
// whether the caller must skip its legacy direct SQS send.
func (s *EventService) BeginCoverProcessingWithOutbox(eventID uuid.UUID, pendingURL string, message dtos.MediaProcessMessage) (*models.Event, string, bool, error) {
	if strings.TrimSpace(message.JobID) == "" {
		return nil, "", false, fmt.Errorf("event cover job ID is required")
	}
	if repository, ok := s.repo.(ports.EventCoverProcessingOutboxRepository); ok && sqsrepository.IsConfiguredFor(false) {
		normalized, err := sqsrepository.NormalizeMediaJob(message)
		if err != nil {
			return nil, "", false, err
		}
		event, previousPending, err := repository.BeginEventCoverProcessingWithOutbox(eventID, pendingURL, normalized.JobID, normalized)
		if err != nil {
			return nil, "", false, err
		}
		_ = invalidateEventsCache(s.cache)
		return event, previousPending, true, nil
	}
	event, previousPending, err := s.BeginCoverProcessing(eventID, pendingURL, message.JobID)
	return event, previousPending, false, err
}

func (s *EventService) ApplyCoverProcessingCallback(eventID uuid.UUID, callback dtos.MediaProcessingCallback) (*models.Event, string, models.MediaVariants, error) {
	repo, ok := s.repo.(ports.EventCoverProcessingRepository)
	if !ok {
		return nil, "", nil, fmt.Errorf("event cover processing repository is not configured")
	}
	callback.ProcessingStatus = strings.ToLower(strings.TrimSpace(callback.ProcessingStatus))
	callback.JobID = strings.TrimSpace(callback.JobID)
	callback.ObjectKey = strings.TrimSpace(callback.ObjectKey)
	if callback.JobID == "" || callback.Generation <= 0 {
		return nil, "", nil, fmt.Errorf("%w: job_id and generation are required", ErrInvalidCoverProcessingTransition)
	}
	if callback.ProcessingStatus != "processing" && callback.ProcessingStatus != "done" && callback.ProcessingStatus != "failed" {
		return nil, "", nil, fmt.Errorf("%w: unsupported status", ErrInvalidCoverProcessingTransition)
	}
	if callback.ProcessingStatus == "done" {
		currentEvent, loadErr := s.repo.GetEventByIDRaw(eventID)
		if loadErr != nil {
			return nil, "", nil, loadErr
		}
		expectedStem := storagekeys.Scoped(
			storagekeys.Namespace(currentEvent.CoverPendingURL),
			fmt.Sprintf("events/%s/covers/%s", eventID, callback.JobID),
		)
		if callback.ObjectKey != expectedStem+".webp" {
			return nil, "", nil, fmt.Errorf("%w: output key is not owned by this cover job", ErrInvalidCoverProcessingTransition)
		}
		seen := map[int]bool{}
		for _, variant := range callback.MediaVariants {
			if variant.Width < 160 || variant.Width > 4096 || variant.Format != "webp" || variant.Bytes < 0 || seen[variant.Width] {
				return nil, "", nil, fmt.Errorf("%w: invalid cover variant", ErrInvalidCoverProcessingTransition)
			}
			seen[variant.Width] = true
			if variant.ObjectKey != fmt.Sprintf("%s-%d.webp", expectedStem, variant.Width) || filepath.Ext(variant.ObjectKey) != ".webp" {
				return nil, "", nil, fmt.Errorf("%w: variant key is not owned by this cover job", ErrInvalidCoverProcessingTransition)
			}
		}
	}
	event, oldCover, oldVariants, err := repo.ApplyEventCoverProcessing(eventID, callback)
	if err != nil {
		return nil, "", nil, err
	}
	_ = invalidateEventsCache(s.cache)
	return event, oldCover, oldVariants, nil
}

func (s *EventService) ClearCoverImage(eventID uuid.UUID) (string, error) {
	oldPath, _, err := s.ClearCoverImageWithVariants(eventID)
	return oldPath, err
}

func (s *EventService) ClearCoverImageWithVariants(eventID uuid.UUID) (string, models.MediaVariants, error) {
	event, err := s.repo.GetEventByIDRaw(eventID)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrEventNotFound, err)
	}
	oldPath := event.CoverImageURL
	if oldPath == "" && len(event.CoverVariants) == 0 {
		return "", event.CoverVariants, nil
	}
	if repo, ok := s.repo.(ports.EventCoverVariantsRepository); ok {
		err = repo.UpdateEventCoverWithVariants(eventID, "", models.MediaVariants{})
	} else {
		err = s.repo.UpdateEventCover(eventID, "")
	}
	if err != nil {
		return "", nil, err
	}
	_ = invalidateEventsCache(s.cache)
	return oldPath, event.CoverVariants, nil
}

func (s *EventService) DeleteEvent(id uuid.UUID) error {
	if err := s.repo.DeleteEvent(id); err != nil {
		return err
	}
	return invalidateEventsCache(s.cache)
}
