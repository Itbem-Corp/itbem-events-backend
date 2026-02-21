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

// GetInvitationByIDLite loads only the base Invitation record without preloads.
// Use this when you only need scalar fields (e.g. MaxGuests in ConfirmRSVP).
func GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	var model models.Invitation
	err := configuration.DB.Where("id = ?", id).First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
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

// InvitationRepo implements ports.InvitationRepository.
type InvitationRepo struct{}

func NewInvitationRepo() *InvitationRepo { return &InvitationRepo{} }

func (r *InvitationRepo) CreateInvitation(m *models.Invitation) error  { return CreateInvitation(m) }
func (r *InvitationRepo) UpdateInvitation(m *models.Invitation) error  { return UpdateInvitation(m) }
func (r *InvitationRepo) DeleteInvitation(id uuid.UUID) error           { return DeleteInvitation(id) }
func (r *InvitationRepo) GetInvitationByID(id uuid.UUID) (*models.Invitation, error) {
	return GetInvitationByID(id)
}
func (r *InvitationRepo) GetInvitationByIDLite(id uuid.UUID) (*models.Invitation, error) {
	return GetInvitationByIDLite(id)
}
func (r *InvitationRepo) ListInvitations() ([]models.Invitation, error) { return ListInvitations() }

func ListInvitationsByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	var list []models.Invitation
	err := configuration.DB.
		Where("invitations.event_id = ?", eventID).
		Find(&list).Error
	return list, err
}

func (r *InvitationRepo) ListByEventID(eventID uuid.UUID) ([]models.Invitation, error) {
	return ListInvitationsByEventID(eventID)
}
