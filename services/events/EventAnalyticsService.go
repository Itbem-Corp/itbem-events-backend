package events

import (
	"context"
	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"fmt"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

var _eventAnalyticsSvc *EventAnalyticsService

func SetDefaultEventAnalyticsService(svc *EventAnalyticsService) {
	_eventAnalyticsSvc = svc
}

type EventAnalyticsService struct {
	repo  ports.EventAnalyticsRepository
	cache ports.CacheRepository
}

type eventAnalyticsDeltaRepository interface {
	AdjustEventAnalytics(eventID uuid.UUID, field string, delta int) error
}

type eventAnalyticsRollupRepository interface {
	GetEventAnalyticsBaseByEventID(eventID uuid.UUID) (*models.EventAnalytics, error)
	GetEventAnalyticsRollupByEventID(eventID uuid.UUID) (*models.EventAnalyticsRollup, error)
}

func NewEventAnalyticsService(repo ports.EventAnalyticsRepository, cache ports.CacheRepository) *EventAnalyticsService {
	return &EventAnalyticsService{repo: repo, cache: cache}
}

func eventAnalyticsServiceUnavailable() error {
	return fmt.Errorf("event analytics service not initialized")
}

func ListEventAnalyticss() ([]models.EventAnalytics, error) {
	if _eventAnalyticsSvc == nil {
		return nil, eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.ListEventAnalyticss()
}

func (s *EventAnalyticsService) ListEventAnalyticss() ([]models.EventAnalytics, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisEventAnalyticsKey,
		utils.CacheTTLs[utils.RedisEventAnalyticsKey],
		s.repo.ListEventAnalyticss,
	)
}

func GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	if _eventAnalyticsSvc == nil {
		return nil, eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.GetEventAnalyticsByID(id)
}

func (s *EventAnalyticsService) GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	return s.repo.GetEventAnalyticsByID(id)
}

func CreateEventAnalytics(obj *models.EventAnalytics) error {
	if _eventAnalyticsSvc == nil {
		return eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.CreateEventAnalytics(obj)
}

func (s *EventAnalyticsService) CreateEventAnalytics(obj *models.EventAnalytics) error {
	if err := s.repo.CreateEventAnalytics(obj); err != nil {
		return err
	}
	return s.invalidateCache()
}

func UpdateEventAnalytics(obj *models.EventAnalytics) error {
	if _eventAnalyticsSvc == nil {
		return eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.UpdateEventAnalytics(obj)
}

func (s *EventAnalyticsService) UpdateEventAnalytics(obj *models.EventAnalytics) error {
	if err := s.repo.UpdateEventAnalytics(obj); err != nil {
		return err
	}
	return s.invalidateCache()
}

func DeleteEventAnalytics(id uuid.UUID) error {
	if _eventAnalyticsSvc == nil {
		return eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.DeleteEventAnalytics(id)
}

func (s *EventAnalyticsService) DeleteEventAnalytics(id uuid.UUID) error {
	if err := s.repo.DeleteEventAnalytics(id); err != nil {
		return err
	}
	return s.invalidateCache()
}

func (s *EventAnalyticsService) invalidateCache() error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Invalidate(utils.RedisEventAnalyticsKey, "all")
	return nil
}

// GetEventAnalyticsByEventID fetches the analytics record for a given event.
func GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	if _eventAnalyticsSvc == nil {
		return nil, eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.GetEventAnalyticsByEventID(eventID)
}

func (s *EventAnalyticsService) GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	return s.repo.GetEventAnalyticsByEventID(eventID)
}

func GetEventAnalyticsBaseByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	if _eventAnalyticsSvc == nil {
		return nil, eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.GetEventAnalyticsBaseByEventID(eventID)
}

func (s *EventAnalyticsService) GetEventAnalyticsBaseByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	if repo, ok := s.repo.(eventAnalyticsRollupRepository); ok {
		return repo.GetEventAnalyticsBaseByEventID(eventID)
	}
	return s.repo.GetEventAnalyticsByEventID(eventID)
}

func GetEventAnalyticsRollupByEventID(eventID uuid.UUID) (*models.EventAnalyticsRollup, error) {
	if _eventAnalyticsSvc == nil {
		return nil, eventAnalyticsServiceUnavailable()
	}
	return _eventAnalyticsSvc.GetEventAnalyticsRollupByEventID(eventID)
}

func (s *EventAnalyticsService) GetEventAnalyticsRollupByEventID(eventID uuid.UUID) (*models.EventAnalyticsRollup, error) {
	repo, ok := s.repo.(eventAnalyticsRollupRepository)
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return repo.GetEventAnalyticsRollupByEventID(eventID)
}

// IncrementAnalytics atomically increments one analytics counter for an event.
// Call as a goroutine -- never blocks the main request.
func IncrementAnalytics(eventID uuid.UUID, field string) {
	AdjustAnalytics(eventID, field, 1)
}

// AdjustAnalytics applies a signed delta to one analytics counter.
// It is best-effort and clamps counters at zero so state transitions never create negative totals.
func AdjustAnalytics(eventID uuid.UUID, field string, delta int) {
	if _eventAnalyticsSvc == nil {
		return
	}
	_eventAnalyticsSvc.AdjustAnalytics(eventID, field, delta)
}

func (s *EventAnalyticsService) IncrementAnalytics(eventID uuid.UUID, field string) {
	s.AdjustAnalytics(eventID, field, 1)
}

func (s *EventAnalyticsService) AdjustAnalytics(eventID uuid.UUID, field string, delta int) {
	if eventID == uuid.Nil || delta == 0 {
		return
	}
	if repo, ok := s.repo.(eventAnalyticsDeltaRepository); ok {
		if err := repo.AdjustEventAnalytics(eventID, field, delta); err == nil {
			_ = s.invalidateCache()
		}
		return
	}
	analytics, err := s.GetEventAnalyticsByEventID(eventID)
	if err != nil || analytics == nil {
		analytics = &models.EventAnalytics{EventID: eventID}
		applyDelta(analytics, field, delta)
		_ = s.CreateEventAnalytics(analytics)
		return
	}
	applyDelta(analytics, field, delta)
	_ = s.UpdateEventAnalytics(analytics)
}

func applyDelta(a *models.EventAnalytics, field string, delta int) {
	switch field {
	case "views":
		a.Views = nonNegativeAnalyticsValue(a.Views + delta)
	case "rsvp_confirmed":
		a.RSVPConfirmed = nonNegativeAnalyticsValue(a.RSVPConfirmed + delta)
	case "rsvp_declined":
		a.RSVPDeclined = nonNegativeAnalyticsValue(a.RSVPDeclined + delta)
	case "moment_comments":
		a.MomentComments = nonNegativeAnalyticsValue(a.MomentComments + delta)
	case "moment_uploads":
		a.MomentUploads = nonNegativeAnalyticsValue(a.MomentUploads + delta)
	}
}

func nonNegativeAnalyticsValue(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
