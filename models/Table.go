package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Table struct {
	ID        uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID   uuid.UUID      `gorm:"type:uuid;index;not null" json:"event_id"`
	Name      string         `gorm:"type:varchar(100);not null" json:"name" validate:"required,min=1,max=100"`
	Capacity  int            `gorm:"not null;default:10" json:"capacity" validate:"required,min=1,max=500"`
	SortOrder int            `gorm:"not null;default:0" json:"sort_order"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
