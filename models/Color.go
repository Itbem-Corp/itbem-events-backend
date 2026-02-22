package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Color struct {
	ID        uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Value     string         `json:"value"`
	Name      string         `json:"name"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
