package models

import (
	"time"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

type EventTable struct {
	ID        uuid.UUID      `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID   uuid.UUID      `gorm:"type:uuid;index;not null" json:"event_id"`
	Event     Event          `gorm:"foreignKey:EventID" json:"-" validate:"-"`
	Name      string         `gorm:"type:varchar(120);not null" json:"name" validate:"required"`
	Capacity  int            `gorm:"default:0" json:"capacity"`
	SortOrder int            `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// TableName preserves the physical table created by the production Table
// model that predates the EventTable rename. Keeping the storage name stable
// lets the old and new backend revisions run side-by-side during candidate
// health checks and makes an immediate container rollback safe.
func (EventTable) TableName() string {
	return "tables"
}
