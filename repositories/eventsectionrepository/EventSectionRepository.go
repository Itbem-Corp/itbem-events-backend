package eventsectionrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateEventSection(m *models.EventSection) error {
    return gormrepository.Insert(m)
}

func UpdateEventSection(m *models.EventSection) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteEventSection(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.EventSection{})
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
        OrderBy:  "order",
        OrderDir: "asc",
    })
    return list, err
}

// EventSectionRepo implements ports.EventSectionRepository.
type EventSectionRepo struct{}

func NewEventSectionRepo() *EventSectionRepo { return &EventSectionRepo{} }

func (r *EventSectionRepo) CreateEventSection(m *models.EventSection) error { return CreateEventSection(m) }
func (r *EventSectionRepo) UpdateEventSection(m *models.EventSection) error { return UpdateEventSection(m) }
func (r *EventSectionRepo) DeleteEventSection(id uuid.UUID) error            { return DeleteEventSection(id) }
func (r *EventSectionRepo) GetEventSectionByID(id uuid.UUID) (*models.EventSection, error) {
    return GetEventSectionByID(id)
}
func (r *EventSectionRepo) ListEventSections() ([]models.EventSection, error) {
    return ListEventSections()
}
func (r *EventSectionRepo) ListByEventID(eventID uuid.UUID) ([]models.EventSection, error) {
    return ListByEventID(eventID)
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
