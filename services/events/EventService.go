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
	var event models.Event
	if err := json.Unmarshal([]byte(jsonStr), &event); err != nil {
		return nil, err
	}
	return &event, nil
}

func (s *EventService) CreateEvent(obj *models.Event) error {
	if obj.Identifier == "" && obj.Name != "" {
		obj.Identifier = s.generateUniqueIdentifier(obj.Name)
	}
	if err := s.repo.CreateEvent(obj); err != nil {
		return err
	}
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

func (s *EventService) UpdateEvent(obj *models.Event) error {
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
