package invitationrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateInvitation(m *models.Invitation) error {
    return gormrepository.Insert(m)
}

func UpdateInvitation(m *models.Invitation) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteInvitation(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.Invitation{})
}

func GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
    var model models.Invitation
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListInvitations() ([]models.Invitation, error) {
    var list []models.Invitation
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}
