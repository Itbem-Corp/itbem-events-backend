package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type EventMember struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID   uuid.UUID `gorm:"type:uuid;index;not null"`
	Event     Event     `gorm:"foreignKey:EventID"`
	UserID    uuid.UUID `gorm:"type:uuid;index;not null"`
	User      User      `gorm:"foreignKey:UserID"`
	Role      string    // Ej: "OWNER", "EDITOR", "VIEWER"
	IsActive  bool      `gorm:"default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
