package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type ResourceType struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Code      string    `json:"code"`  // Ej: "image", "video", "audio"
	Label     string    `json:"label"` // Ej: "Imagen", "Video", "Audio"
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
