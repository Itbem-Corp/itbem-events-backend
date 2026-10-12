package eventsectionrepository

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/services/ports"
	"fmt"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"sort"
	"time"
)

func CreateEventSection(m *models.EventSection) error {
	return gormrepository.Insert(m)
}

func UpdateEventSection(m *models.EventSection) error {
	return gormrepository.Update(m, m.ID)
}

// UpdateEventSectionIfUnmodified writes the section only while its stored
// updated_at equals expected. Postgres keeps microseconds, so expected is
// truncated to that precision; the check and the write are one UPDATE, so
// two concurrent saves with the same expected value cannot both win.
func UpdateEventSectionIfUnmodified(m *models.EventSection, expected time.Time) error {
	result := configuration.DB.Model(m).
		Where("id = ? AND updated_at = ?", m.ID, expected.UTC().Truncate(time.Microsecond)).
		Select("*").Updates(m)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ports.ErrEventSectionConflict
	}
	return nil
}

func DeleteEventSection(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.EventSection{})
}

func BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	if len(updates) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})

	return gormrepository.DB().Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			result := tx.Model(&models.EventSection{}).
				Where("id = ? AND event_id = ?", id, eventID).
				Update("\"order\"", updates[id])
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("section %s not found for event %s", id.String(), eventID.String())
			}
		}
		return nil
	})
}

func GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	var model models.EventSection
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListEventSections() ([]models.EventSection, error) {
	var list []models.EventSection
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

func ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	var list []models.EventSection
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{
		Filters:  map[string]interface{}{"event_id": eventID},
		OrderBy:  `"order"`,
		OrderDir: "asc",
	})
	return list, err
}

// EventSectionRepo implements ports.EventSectionRepository.
type EventSectionRepo struct{}

func NewEventSectionRepo() *EventSectionRepo { return &EventSectionRepo{} }

func (r *EventSectionRepo) CreateEventSection(m *models.EventSection) error {
	return CreateEventSection(m)
}
func (r *EventSectionRepo) UpdateEventSection(m *models.EventSection) error {
	return UpdateEventSection(m)
}
func (r *EventSectionRepo) UpdateEventSectionIfUnmodified(m *models.EventSection, expected time.Time) error {
	return UpdateEventSectionIfUnmodified(m, expected)
}
func (r *EventSectionRepo) DeleteEventSection(id uuid.UUID) error { return DeleteEventSection(id) }
func (r *EventSectionRepo) BulkUpdateSectionOrder(eventID uuid.UUID, updates map[uuid.UUID]int) error {
	return BulkUpdateSectionOrder(eventID, updates)
}
func (r *EventSectionRepo) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
	return GetEventSectionByID(id)
}
func (r *EventSectionRepo) ListEventSections() ([]models.EventSection, error) {
	return ListEventSections()
}
func (r *EventSectionRepo) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
	return ListByEventID(eventID)
}
func (r *EventSectionRepo) ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error) {
	return ListByEventIDForSpec(eventID)
}

// ListByEventIDForSpec returns visible SDUI sections for an event, ordered by position.
// Only sections with a non-empty component_type are included.
func ListByEventIDForSpec(eventID uuid.UUID) ([]models.EventSection, error) {
	var list []models.EventSection
	err := gormrepository.DB().
		Where("event_id = ? AND is_visible = ? AND component_type != ?", eventID, true, "").
		Order("\"order\" ASC").
		Find(&list).Error
	return list, err
}
