package invitationrepository

import (
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

func DB() *gorm.DB {
	return configuration.DB
}

func CreateInvitation(m *models.Invitation) error {
	return gormrepository.Insert(m)
}

func CreateManyInvitations(models []models.Invitation) error {
	return gormrepository.InsertMany(models)
}

func UpdateInvitation(m *models.Invitation) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteInvitation(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Invitation{})
}

func GetGuestByInvitationID(invitationID uuid.UUID) (*models.Guest, error) {
	var guest models.Guest
	err := configuration.DB.
		Preload("GuestStatus").
		Preload("Event").
		Preload("Event.EventType").
		Preload("Event.EventConfig").
		Where("invitation_id = ?", invitationID).
		First(&guest).Error

	if err != nil {
		return nil, err
	}
	return &guest, nil
}

func GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	var model models.Invitation
	err := configuration.DB.
		Preload("Event").
		Preload("Event.EventType").
		Preload("Event.EventConfig").
		Preload("Event.EventConfig.DesignTemplate").
		Preload("Event.EventConfig.DesignTemplate.ColorPalette").
		Preload("Event.EventConfig.DesignTemplate.FontSet").
		Where("id = ?", id).
		First(&model).Error

	if err != nil {
		return nil, err
	}
	return &model, nil
}

func ListInvitations() ([]models.Invitation, error) {
	var list []models.Invitation
	err := configuration.DB.
		Preload("Event").
		Preload("Event.EventType").
		Preload("Event.EventConfig").
		Preload("Event.EventConfig.DesignTemplate").
		Preload("Event.EventConfig.DesignTemplate.ColorPalette").
		Preload("Event.EventConfig.DesignTemplate.FontSet").
		Find(&list).Error
	return list, err
}
