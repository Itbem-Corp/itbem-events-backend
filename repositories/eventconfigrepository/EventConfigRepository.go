package eventconfigrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateEventConfig(m *models.EventConfig) error {
    return gormrepository.Insert(m)
}

func UpdateEventConfig(m *models.EventConfig) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteEventConfig(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.EventConfig{})
}

func GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
    var model models.EventConfig
    err := gormrepository.GetByID(&model, id,
        "DesignTemplate",
        "DesignTemplate.ColorPalette",
        "DesignTemplate.ColorPalette.Patterns",
        "DesignTemplate.ColorPalette.Patterns.Color",
        "DesignTemplate.FontSet",
        "DesignTemplate.FontSet.Patterns",
        "DesignTemplate.FontSet.Patterns.Font",
    )
    return &model, err
}

func ListEventConfigs() ([]models.EventConfig, error) {
    var list []models.EventConfig
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}

// EventConfigRepo implements ports.EventConfigRepository.
type EventConfigRepo struct{}

func NewEventConfigRepo() *EventConfigRepo { return &EventConfigRepo{} }

func (r *EventConfigRepo) CreateEventConfig(m *models.EventConfig) error  { return CreateEventConfig(m) }
func (r *EventConfigRepo) UpdateEventConfig(m *models.EventConfig) error  { return UpdateEventConfig(m) }
func (r *EventConfigRepo) DeleteEventConfig(id uuid.UUID) error            { return DeleteEventConfig(id) }
func (r *EventConfigRepo) GetEventConfigByID(id uuid.UUID) (*models.EventConfig, error) {
    return GetEventConfigByID(id)
}
