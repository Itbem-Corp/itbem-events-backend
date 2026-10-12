package events

import (
	"context"
	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"sort"
	"time"

	"github.com/gofrs/uuid"
)

// _eventSectionSvc is the package-level singleton set by internal/app.
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
func BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	return _eventSectionSvc.BulkUpdateSectionOrder(eventID, updates)
}
func ListEventSectionsByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	return _eventSectionSvc.ListByEventID(eventID)
}

// EventSectionService is the injectable, struct-based event section service.
type EventSectionService struct {
	repo  ports.EventSectionRepository
	cache ports.CacheRepository
}

func NewEventSectionService(repo ports.EventSectionRepository, cache ports.CacheRepository) *EventSectionService {
	return &EventSectionService{repo: repo, cache: cache}
}

func (s *EventSectionService) ListEventSections() ([]models.EventSection, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisEventSectionsKey,
		utils.CacheTTLs[utils.RedisEventSectionsKey],
		s.repo.ListEventSections,
	)
}

func (s *EventSectionService) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return s.repo.GetEventSectionByID(id)
}

func (s *EventSectionService) CreateEventSection(obj *models.EventSection) error {
	if err := s.repo.CreateEventSection(obj); err != nil {
		return err
	}
	return s.invalidateEventSectionCache()
}

func (s *EventSectionService) UpdateEventSection(obj *models.EventSection) error {
	if err := s.repo.UpdateEventSection(obj); err != nil {
		return err
	}
	return s.invalidateEventSectionCache()
}

// UpdateEventSectionIfUnmodified saves obj only when the stored section still
// has updated_at == expected (microsecond precision); otherwise it returns
// ports.ErrEventSectionConflict and nothing is written (STU-N3).
func (s *EventSectionService) UpdateEventSectionIfUnmodified(obj *models.EventSection, expected time.Time) error {
	if conditional, ok := s.repo.(ports.ConditionalEventSectionUpdater); ok {
		if err := conditional.UpdateEventSectionIfUnmodified(obj, expected); err != nil {
			return err
		}
		return s.invalidateEventSectionCache()
	}
	// Repositories without a conditional write (test doubles) get a
	// read-compare-write; the production repository is atomic.
	current, err := s.repo.GetEventSectionByID(obj.ID)
	if err != nil {
		return err
	}
	if !SectionUpdatedAtMatches(current.UpdatedAt, expected) {
		return ports.ErrEventSectionConflict
	}
	return s.UpdateEventSection(obj)
}

// SectionUpdatedAtMatches compares two updated_at values at the precision
// Postgres stores (microseconds).
func SectionUpdatedAtMatches(stored, expected time.Time) bool {
	return stored.UTC().Truncate(time.Microsecond).Equal(expected.UTC().Truncate(time.Microsecond))
}

func (s *EventSectionService) DeleteEventSection(id uuid.UUID) error {
	if err := s.repo.DeleteEventSection(id); err != nil {
		return err
	}
	return s.invalidateEventSectionCache()
}

func (s *EventSectionService) BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	if len(updates) == 0 {
		return nil
	}
	if err := s.repo.BulkUpdateSectionOrder(eventID, updates); err != nil {
		return err
	}
	return s.invalidateEventSectionCache()
}

func (s *EventSectionService) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	sections, err := s.repo.ListByEventID(eventID)
	if err != nil {
		return nil, err
	}
	sortEventSectionsByRenderOrder(sections)
	return sections, nil
}

func (s *EventSectionService) invalidateEventSectionCache() error {
	if s.cache == nil {
		return nil
	}
	_ = s.cache.Invalidate(utils.RedisEventSectionsKey, "all")
	return nil
}

func sortEventSectionsByRenderOrder(sections []models.EventSection) {
	sort.SliceStable(sections, func(i, j int) bool {
		if sections[i].Order != sections[j].Order {
			return sections[i].Order < sections[j].Order
		}
		return sections[i].ID.String() < sections[j].ID.String()
	})
}
