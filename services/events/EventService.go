package events

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
)

// _eventSvc is the package-level singleton set by server.go.
var _eventSvc *EventService

// SetDefaultEventService wires the package-level functions to the DI instance.
func SetDefaultEventService(svc *EventService) { _eventSvc = svc }

func ListEvents() ([]models.Event, error)              { return _eventSvc.ListEvents() }
func GetEventByID(id uuid.UUID) (*models.Event, error) { return _eventSvc.GetEventByID(id) }
func CreateEvent(obj *models.Event) error              { return _eventSvc.CreateEvent(obj) }
func UpdateEvent(obj *models.Event) error              { return _eventSvc.UpdateEvent(obj) }
func DeleteEvent(id uuid.UUID) error                   { return _eventSvc.DeleteEvent(id) }

// EventService is the injectable, struct-based events service.
type EventService struct {
	repo  ports.EventsRepository
	cache ports.CacheRepository
}

func NewEventService(repo ports.EventsRepository, cache ports.CacheRepository) *EventService {
	return &EventService{repo: repo, cache: cache}
}

func (s *EventService) ListEvents() ([]models.Event, error) {
	cacheKey := "all:events"
	ctx := context.Background()
	cached, err := s.cache.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Event
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}
	data, err := s.repo.ListEvents(1, 0, "")
	if err != nil {
		return nil, err
	}
	jsonStr, _ := json.Marshal(data)
	_ = s.cache.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["events"])
	return data, nil
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
	return s.cache.Invalidate("events", "all")
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
		_ = CreateEventConfig(&models.EventConfig{ID: eventID})
	}
	_ = CreateEventAnalytics(&models.EventAnalytics{EventID: eventID})
}

func (s *EventService) UpdateEvent(obj *models.Event) error {
	// If identifier was not provided in the update payload (empty string),
	// restore the existing identifier from DB to prevent GORM Select("*")
	// from zeroing it out and breaking distributed QR codes and links.
	if obj.Identifier == "" {
		existing, err := s.GetEventByID(obj.ID)
		if err != nil {
			return fmt.Errorf("UpdateEvent: could not fetch existing identifier: %w", err)
		}
		if existing != nil && existing.Identifier != "" {
			obj.Identifier = existing.Identifier
		}
	}
	if err := s.repo.UpdateEvent(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("events", "all")
}

func (s *EventService) DeleteEvent(id uuid.UUID) error {
	if err := s.repo.DeleteEvent(id); err != nil {
		return err
	}
	return s.cache.Invalidate("events", "all")
}
