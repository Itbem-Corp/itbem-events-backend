package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Font struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name      string    // Ej: "Playfair Display"
	FileURL   string    // URL al archivo .woff/.ttf en S3
	IsSerif   bool      // Para clasificarlas (útil en UI)
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
