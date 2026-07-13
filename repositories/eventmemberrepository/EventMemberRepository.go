package eventmemberrepository

import (
	"events-stocks/models"
	"strings"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type EventMemberRepo struct{ db *gorm.DB }

func NewEventMemberRepo(db *gorm.DB) *EventMemberRepo { return &EventMemberRepo{db: db} }

func (r *EventMemberRepo) RoleForUser(eventID, userID uuid.UUID) (string, bool, error) {
	var member models.EventMember
	err := r.db.Where("event_id = ? AND user_id = ? AND is_active = true", eventID, userID).First(&member).Error
	if err == nil {
		return strings.ToUpper(strings.TrimSpace(member.Role)), true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return "", false, nil
	}
	return "", false, err
}

func (r *EventMemberRepo) List(eventID uuid.UUID) ([]models.EventMember, error) {
	var members []models.EventMember
	err := r.db.Preload("User").Where("event_id = ? AND is_active = true", eventID).Order("created_at ASC").Find(&members).Error
	return members, err
}

func (r *EventMemberRepo) Upsert(eventID, userID uuid.UUID, role string) (*models.EventMember, error) {
	var member models.EventMember
	err := r.db.Where("event_id = ? AND user_id = ?", eventID, userID).First(&member).Error
	if err == gorm.ErrRecordNotFound {
		member = models.EventMember{EventID: eventID, UserID: userID, Role: role, IsActive: true}
		if err := r.db.Create(&member).Error; err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if err := r.db.Model(&member).Updates(map[string]interface{}{"role": role, "is_active": true}).Error; err != nil {
		return nil, err
	}
	if err := r.db.Preload("User").First(&member, "id = ?", member.ID).Error; err != nil {
		return nil, err
	}
	return &member, nil
}

func (r *EventMemberRepo) Remove(eventID, userID uuid.UUID) error {
	return r.db.Where("event_id = ? AND user_id = ?", eventID, userID).Delete(&models.EventMember{}).Error
}
