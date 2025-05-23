package invitationaccesstokenrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateInvitationAccessToken(m *models.InvitationAccessToken) error {
    return gormrepository.Insert(m)
}

func UpdateInvitationAccessToken(m *models.InvitationAccessToken) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteInvitationAccessToken(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.InvitationAccessToken{})
}

func GetInvitationAccessTokenByID(id uuid.UUID) (*models.InvitationAccessToken, error) {
    var model models.InvitationAccessToken
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListInvitationAccessTokens() ([]models.InvitationAccessToken, error) {
    var list []models.InvitationAccessToken
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}
