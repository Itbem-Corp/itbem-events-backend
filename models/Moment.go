package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Moment struct {
	ID           uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	InvitationID uuid.UUID  `gorm:"type:uuid;index"`
	Invitation   Invitation `gorm:"foreignKey:InvitationID"`
	MomentTypeID uuid.UUID  `gorm:"type:uuid;index"`
	MomentType   MomentType `gorm:"foreignKey:MomentTypeID"`
	GuestID      *uuid.UUID
	Guest        *Guest `gorm:"foreignKey:GuestID"`
	Title        string
	Description  string // puede ser texto, caption o descripción del momento
	ContentURL   string // si aplica: imagen, video, audio
	IsApproved   bool   `gorm:"default:false"` // para moderación
	Order        int    // opcional: ordenar por prioridad o fecha
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}
