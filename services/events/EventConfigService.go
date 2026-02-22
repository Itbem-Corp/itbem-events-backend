package events

import (
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// _eventConfigSvc is the package-level singleton set by server.go.
var _eventConfigSvc *EventConfigService

// SetDefaultEventConfigService wires the package-level functions to the DI instance.
func SetDefaultEventConfigService(svc *EventConfigService) { _eventConfigSvc = svc }

func GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	return _eventConfigSvc.GetEventConfigByID(id)
}
func CreateEventConfig(obj *models.EventConfig) error { return _eventConfigSvc.CreateEventConfig(obj) }
func UpdateEventConfig(obj *models.EventConfig) error { return _eventConfigSvc.UpdateEventConfig(obj) }
func DeleteEventConfig(id uuid.UUID) error            { return _eventConfigSvc.DeleteEventConfig(id) }

// EventConfigService is the injectable, struct-based event config service.
type EventConfigService struct {
	repo  ports.EventConfigRepository
	cache ports.CacheRepository
}

func NewEventConfigService(repo ports.EventConfigRepository, cache ports.CacheRepository) *EventConfigService {
	return &EventConfigService{repo: repo, cache: cache}
}

func (s *EventConfigService) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	config, err := s.repo.GetEventConfigByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Auto-create a default config with the same ID as the event
			defaultConfig := &models.EventConfig{ID: id}
			if createErr := s.repo.CreateEventConfig(defaultConfig); createErr != nil {
				return nil, createErr
			}
			// Re-fetch with preloads
			return s.repo.GetEventConfigByID(id)
		}
		return nil, err
	}
	return config, nil
}

func (s *EventConfigService) CreateEventConfig(obj *models.EventConfig) error {
	if err := s.repo.CreateEventConfig(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("events", "all")
}

func (s *EventConfigService) UpdateEventConfig(obj *models.EventConfig) error {
	if err := s.repo.UpdateEventConfig(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("events", "all")
}

func (s *EventConfigService) DeleteEventConfig(id uuid.UUID) error {
	if err := s.repo.DeleteEventConfig(id); err != nil {
		return err
	}
	return s.cache.Invalidate("events", "all")
}
