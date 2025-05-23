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
