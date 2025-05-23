package gueststatusrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateGuestStatus(m *models.GuestStatus) error {
    return gormrepository.Insert(m)
}

func UpdateGuestStatus(m *models.GuestStatus) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteGuestStatus(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.GuestStatus{})
}

func GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
    var model models.GuestStatus
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListGuestStatuss() ([]models.GuestStatus, error) {
    var list []models.GuestStatus
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}
