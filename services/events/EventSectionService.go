package events

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

// _eventSectionSvc is the package-level singleton set by server.go.
var _eventSectionSvc *EventSectionService

// SetDefaultEventSectionService wires the package-level functions to the DI instance.
func SetDefaultEventSectionService(svc *EventSectionService) { _eventSectionSvc = svc }

func ListEventSections() ([]models.EventSection, error) { return _eventSectionSvc.ListEventSections() }
func GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return _eventSectionSvc.GetEventSectionByID(id)
}
func CreateEventSection(obj *models.EventSection) error {
	return _eventSectionSvc.CreateEventSection(obj)
}
func UpdateEventSection(obj *models.EventSection) error {
	return _eventSectionSvc.UpdateEventSection(obj)
}
func DeleteEventSection(id uuid.UUID) error { return _eventSectionSvc.DeleteEventSection(id) }

// EventSectionService is the injectable, struct-based event section service.
type EventSectionService struct {
	repo  ports.EventSectionRepository
	cache ports.CacheRepository
}

func NewEventSectionService(repo ports.EventSectionRepository, cache ports.CacheRepository) *EventSectionService {
	return &EventSectionService{repo: repo, cache: cache}
}

func (s *EventSectionService) ListEventSections() ([]models.EventSection, error) {
	cacheKey := "all:event_sections"
	ctx := context.Background()
	cached, err := s.cache.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.EventSection
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}
	data, err := s.repo.ListEventSections()
	if err != nil {
		return nil, err
	}
	jsonStr, _ := json.Marshal(data)
	_ = s.cache.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["events"])
	return data, nil
}

func (s *EventSectionService) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return s.repo.GetEventSectionByID(id)
}

func (s *EventSectionService) CreateEventSection(obj *models.EventSection) error {
	if err := s.repo.CreateEventSection(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("event_sections", "all")
}

func (s *EventSectionService) UpdateEventSection(obj *models.EventSection) error {
	if err := s.repo.UpdateEventSection(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("event_sections", "all")
}

func (s *EventSectionService) DeleteEventSection(id uuid.UUID) error {
	if err := s.repo.DeleteEventSection(id); err != nil {
		return err
	}
	return s.cache.Invalidate("event_sections", "all")
}

func (s *EventSectionService) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	return s.repo.ListByEventID(eventID)
}
