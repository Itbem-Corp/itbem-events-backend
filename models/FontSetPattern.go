package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type FontSetPattern struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Key       string    // Ej: "title", "body", "accent"
	FontSetID uuid.UUID `gorm:"type:uuid;index"`
	Order     int
	FontID    uuid.UUID `gorm:"type:uuid;index"`
	Font      Font      `gorm:"foreignKey:FontID"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
