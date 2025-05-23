package guestrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateGuest(m *models.Guest) error {
    return gormrepository.Insert(m)
}

func UpdateGuest(m *models.Guest) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteGuest(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.Guest{})
}

func GetGuestByID(id uuid.UUID) (*models.Guest, error) {
    var model models.Guest
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListGuests() ([]models.Guest, error) {
    var list []models.Guest
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}
