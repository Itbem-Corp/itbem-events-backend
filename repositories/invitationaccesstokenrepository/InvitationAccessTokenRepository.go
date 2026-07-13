package invitationaccesstokenrepository

import (
	"crypto/rand"
	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"fmt"
	"github.com/gofrs/uuid"
	"math/big"
)

const rsvpCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// sin 0, O, I, 1 para evitar confusión visual

func CreateInvitationAccessToken(m *models.InvitationAccessToken) error {
	return gormrepository.Insert(m)
}

func CreateManyInvitationAccessTokens(models []models.InvitationAccessToken) error {
	return gormrepository.InsertMany(models)
}

func GetByToken(token string) (*models.InvitationAccessToken, error) {
	var model models.InvitationAccessToken
	err := configuration.DB.
		Where("token = ? OR pretty_token = ?", token, token).
		First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	var model models.InvitationAccessToken
	err := configuration.DB.
		Where("pretty_token = ?", code).
		First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func GetByInvitationID(invitationID uuid.UUID) (*models.InvitationAccessToken, error) {
	var model models.InvitationAccessToken
	err := configuration.DB.
		Where("invitation_id = ?", invitationID).
		First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	if length < 8 || length > 64 {
		return "", fmt.Errorf("RSVP code length must be between 8 and 64")
	}
	for attempt := 0; attempt < 128; attempt++ {
		code, err := GenerateRSVPCode(length)
		if err != nil {
			return "", err
		}
		exists, err := ExistsPrettyToken(eventID, code)
		if err != nil {
			return "", err
		}
		if !exists {
			return code, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique RSVP code")
}

func GenerateRSVPCode(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("RSVP code length must be positive")
	}
	code := make([]byte, length)
	for i := range code {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(len(rsvpCharset))))
		if err != nil {
			return "", fmt.Errorf("generate cryptographically secure RSVP code: %w", err)
		}
		code[i] = rsvpCharset[index.Int64()]
	}
	return string(code), nil
}

func ExistsPrettyToken(eventID uuid.UUID, code string) (bool, error) {
	var count int64
	err := gormrepository.DB().
		Model(&models.InvitationAccessToken{}).
		Joins("JOIN invitations ON invitations.id = invitation_access_tokens.invitation_id").
		Where("invitations.event_id = ? AND invitation_access_tokens.pretty_token = ?", eventID, code).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

// AccessTokenRepo implements ports.AccessTokenRepository.
type AccessTokenRepo struct{}

func NewAccessTokenRepo() *AccessTokenRepo { return &AccessTokenRepo{} }

func (r *AccessTokenRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	return GetByToken(token)
}
func (r *AccessTokenRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	return GetByPrettyToken(code)
}
func (r *AccessTokenRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return GeneratePrettyToken(eventID, length)
}
func (r *AccessTokenRepo) GetByInvitationID(invitationID uuid.UUID) (*models.InvitationAccessToken, error) {
	return GetByInvitationID(invitationID)
}
func (r *AccessTokenRepo) CreateInvitationAccessToken(token *models.InvitationAccessToken) error {
	return CreateInvitationAccessToken(token)
}
func (r *AccessTokenRepo) UpdateInvitationAccessToken(token *models.InvitationAccessToken) error {
	return UpdateInvitationAccessToken(token)
}
