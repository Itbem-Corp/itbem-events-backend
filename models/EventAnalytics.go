package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type EventAnalytics struct {
	ID             uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID        uuid.UUID `gorm:"type:uuid;uniqueIndex"`
	Event          Event     `gorm:"foreignKey:EventID"`
	Views          int
	MomentComments int
	MomentUploads  int
	RSVPConfirmed  int
	RSVPDeclined   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
