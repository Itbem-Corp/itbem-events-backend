package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type ResourceType struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Code      string    // Ej: "image", "video", "audio"
	Label     string    // Ej: "Imagen", "Video", "Audio"
	CreatedAt time.Time
	UpdatedAt time.Time
}
