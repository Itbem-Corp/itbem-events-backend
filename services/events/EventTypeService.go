package events

import (
	"context"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

var _eventTypeSvc *EventTypeService

func SetDefaultEventTypeService(svc *EventTypeService) { _eventTypeSvc = svc }

func eventTypeServiceUnavailable() error {
	return fmt.Errorf("event type service not initialized")
}

func ListEventTypes() ([]models.EventType, error) {
	if _eventTypeSvc == nil {
		return nil, eventTypeServiceUnavailable()
	}
	return _eventTypeSvc.ListEventTypes()
}

func GetEventTypeByID(id uuid.UUID) (*models.EventType, error) {
	if _eventTypeSvc == nil {
		return nil, eventTypeServiceUnavailable()
	}
	return _eventTypeSvc.GetEventTypeByID(id)
}

func CreateEventType(obj *models.EventType) error {
	if _eventTypeSvc == nil {
		return eventTypeServiceUnavailable()
	}
	return _eventTypeSvc.CreateEventType(obj)
}

func UpdateEventType(obj *models.EventType) error {
	if _eventTypeSvc == nil {
		return eventTypeServiceUnavailable()
	}
	return _eventTypeSvc.UpdateEventType(obj)
}

func DeleteEventType(id uuid.UUID) error {
	if _eventTypeSvc == nil {
		return eventTypeServiceUnavailable()
	}
	return _eventTypeSvc.DeleteEventType(id)
}

type EventTypeService struct {
	repo  ports.EventTypeRepository
	cache ports.CacheRepository
}

func NewEventTypeService(repo ports.EventTypeRepository, cache ports.CacheRepository) *EventTypeService {
	return &EventTypeService{repo: repo, cache: cache}
}

func (s *EventTypeService) ListEventTypes() ([]models.EventType, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisEventTypesKey,
		utils.CacheTTLs[utils.RedisEventTypesKey],
		s.repo.ListEventTypes,
	)
}

func (s *EventTypeService) GetEventTypeByID(id uuid.UUID) (*models.EventType, error) {
	return s.repo.GetEventTypeByID(id)
}

func (s *EventTypeService) CreateEventType(obj *models.EventType) error {
	if err := s.repo.CreateEventType(obj); err != nil {
		return err
	}
	return s.invalidateCache()
}

func (s *EventTypeService) UpdateEventType(obj *models.EventType) error {
	if err := s.repo.UpdateEventType(obj); err != nil {
		return err
	}
	return s.invalidateCache()
}

func (s *EventTypeService) DeleteEventType(id uuid.UUID) error {
	if err := s.repo.DeleteEventType(id); err != nil {
		return err
	}
	return s.invalidateCache()
}

func (s *EventTypeService) invalidateCache() error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Invalidate(utils.RedisEventTypesKey, "all")
	return nil
}
