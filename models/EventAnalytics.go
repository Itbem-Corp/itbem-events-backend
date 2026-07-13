package models

import (
	"github.com/gofrs/uuid"
	"time"
)

type EventAnalytics struct {
	ID             uuid.UUID `json:"id" gorm:"type:uuid;default:uuid_generate_v4();primaryKey"`
	EventID        uuid.UUID `json:"event_id" gorm:"type:uuid;uniqueIndex"`
	Event          Event     `json:"-" gorm:"foreignKey:EventID"`
	Views          int       `json:"views"`
	MomentComments int       `json:"moment_comments"`
	MomentUploads  int       `json:"moment_uploads"`
	MomentTotal    int       `json:"moment_total" gorm:"->;-:migration"`
	MomentApproved int       `json:"moment_approved" gorm:"->;-:migration"`
	MomentPending  int       `json:"moment_pending" gorm:"->;-:migration"`
	RSVPConfirmed  int       `json:"rsvp_confirmed"`
	RSVPDeclined   int       `json:"rsvp_declined"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
