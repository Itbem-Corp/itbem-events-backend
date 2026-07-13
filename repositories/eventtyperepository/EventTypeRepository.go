package eventtyperepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func CreateEventType(m *models.EventType) error {
	return gormrepository.Insert(m)
}

func UpdateEventType(m *models.EventType) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteEventType(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.EventType{})
}

func GetEventTypeByID(id uuid.UUID) (*models.EventType, error) {
	var model models.EventType
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListEventTypes() ([]models.EventType, error) {
	var list []models.EventType
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{
		OrderBy:  "name",
		OrderDir: "asc",
	})
	return list, err
}

// EventTypeRepo implements ports.EventTypeRepository.
type EventTypeRepo struct{}

func NewEventTypeRepo() *EventTypeRepo { return &EventTypeRepo{} }

func (r *EventTypeRepo) CreateEventType(m *models.EventType) error {
	return CreateEventType(m)
}
func (r *EventTypeRepo) UpdateEventType(m *models.EventType) error {
	return UpdateEventType(m)
}
func (r *EventTypeRepo) DeleteEventType(id uuid.UUID) error {
	return DeleteEventType(id)
}
func (r *EventTypeRepo) GetEventTypeByID(id uuid.UUID) (*models.EventType, error) {
	return GetEventTypeByID(id)
}
func (r *EventTypeRepo) ListEventTypes() ([]models.EventType, error) {
	return ListEventTypes()
}
