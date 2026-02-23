package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type EventAnalytics struct {
	ID             uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID        uuid.UUID `gorm:"type:uuid;uniqueIndex"`
	Event          Event     `gorm:"foreignKey:EventID"`
	Views          int       `json:"views"`
	MomentComments int       `json:"moment_comments"`
	MomentUploads  int       `json:"moment_uploads"`
	RSVPConfirmed  int       `json:"rsvp_confirmed"`
	RSVPDeclined   int       `json:"rsvp_declined"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
