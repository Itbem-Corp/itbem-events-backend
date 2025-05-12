// models/events.go
package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Event struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	Name      string    `gorm:"uniqueIndex"`
	Address   string
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
}
