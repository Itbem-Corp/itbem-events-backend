package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type User struct {
	ID           uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	CognitoSub   string         `gorm:"uniqueIndex;not null" json:"-"`
	Email        string         `gorm:"index" json:"email"`
	FirstName    string         `json:"first_name"`
	LastName     string         `json:"last_name"`
	ProfileImage string         `gorm:"text" json:"profile_image"`
	IsActive     bool           `gorm:"default:true" json:"is_active"`
	IsRoot       bool           `gorm:"default:false" json:"-"`
	// Relaciones (Has Many)
	EventMembers  []EventMember  `gorm:"foreignKey:UserID" json:"event_members,omitempty"`
	ClientMembers []ClientMember `gorm:"foreignKey:UserID" json:"client_members,omitempty"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
