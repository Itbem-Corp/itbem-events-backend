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
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListEventConfigs() ([]models.EventConfig, error) {
    var list []models.EventConfig
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}
