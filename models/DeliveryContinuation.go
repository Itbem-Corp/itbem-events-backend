package models

import (
	"github.com/gofrs/uuid"
	"time"
)

// DeliveryContinuation is a durable instruction created in the same transaction
// as an authorized transition. It survives API restarts and browser disconnects.
type DeliveryContinuation struct {
	ID                 uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkItemID         uuid.UUID `gorm:"type:uuid;not null;index" json:"work_item_id"`
	Epoch              int64     `gorm:"not null" json:"epoch"`
	Phase              string    `gorm:"type:varchar(32);not null" json:"phase"`
	RequestedBy        string    `gorm:"type:varchar(128);not null" json:"-"`
	PublicationGrantID string    `gorm:"type:varchar(64);not null;default:''" json:"-"`
	Status             string    `gorm:"type:varchar(24);not null;index" json:"status"`
	Attempts           int       `gorm:"not null;default:0" json:"attempts"`
	AvailableAt        time.Time `gorm:"not null;index" json:"available_at"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
