package events

import (
	"errors"
	"events-stocks/models"
	"events-stocks/services/ports"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// _eventConfigSvc is the package-level singleton set by internal/app.
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

// NewDefaultEventConfig returns the backend defaults expected by the dashboard
// and public PageSpec renderer when an event has no explicit configuration yet.
func NewDefaultEventConfig(id uuid.UUID) *models.EventConfig {
	defaultTemplateID := models.DefaultDesignTemplateID()
	return &models.EventConfig{
		ID:                   id,
		DesignTemplateID:     &defaultTemplateID,
		ShowCountdown:        true,
		ShowRSVPSection:      true,
		ShowEventLocation:    true,
		ShowSecondLocation:   true,
		ShowHostsSection:     true,
		ShowPhotoGallery:     true,
		ShowMomentWall:       true,
		VisibilityConfigured: true,
		ShowContactSection:   true,
		ShowHeader:           true,
		ShowFooter:           true,
		ShowEventSchedule:    true,
		MaxUploadsPerGuest:   30,
		AutoApproveUploads:   false,
		IsPublic:             false,
		IsAuthPreview:        false,
		AllowUploads:         false,
		AllowMessages:        false,
		ShareUploadsEnabled:  false,
		NotifyOnMomentUpload: false,
	}
}

func (s *EventConfigService) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
	config, err := s.repo.GetEventConfigByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Auto-create a default config with the same ID as the event
			defaultConfig := NewDefaultEventConfig(id)
			if createErr := s.repo.CreateEventConfig(defaultConfig); createErr != nil {
				return nil, createErr
			}
			// Re-fetch with preloads
			created, err := s.repo.GetEventConfigByID(id)
			if err != nil {
				return nil, err
			}
			return created.WithVisibilityDefaults(), nil
		}
		return nil, err
	}
	return config.WithVisibilityDefaults(), nil
}

func (s *EventConfigService) CreateEventConfig(obj *models.EventConfig) error {
	if err := s.repo.CreateEventConfig(obj); err != nil {
		return err
	}
	return invalidateEventsCache(s.cache)
}

func (s *EventConfigService) UpdateEventConfig(obj *models.EventConfig) error {
	if err := s.repo.UpdateEventConfig(obj); err != nil {
		return err
	}
	return invalidateEventsCache(s.cache)
}

func (s *EventConfigService) DeleteEventConfig(id uuid.UUID) error {
	if err := s.repo.DeleteEventConfig(id); err != nil {
		return err
	}
	return invalidateEventsCache(s.cache)
}
