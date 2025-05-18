package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type ColorPalettePattern struct {
	ID             uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	ColorPaletteID uuid.UUID `gorm:"type:uuid;index"`
	ColorID        uuid.UUID `gorm:"type:uuid;index"`
	Key            string
	Order          int
	Color          Color `gorm:"foreignKey:ColorID"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}
