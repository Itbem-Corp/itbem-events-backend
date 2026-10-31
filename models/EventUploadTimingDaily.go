package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventUploadTimingDaily stores a fixed histogram bucket of client-measured
// guest upload phase durations (ms). Only closed enumerations are persisted
// (phase, kind, platform, outcome): no IP, user agent, file name, MIME type,
// size or guest identifier is retained.
type EventUploadTimingDaily struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_event_upload_timing" json:"event_id"`
	BucketDate  time.Time `gorm:"type:date;not null;uniqueIndex:idx_event_upload_timing" json:"bucket_date"`
	Phase       string    `gorm:"type:varchar(16);not null;uniqueIndex:idx_event_upload_timing" json:"phase"`
	Kind        string    `gorm:"type:varchar(8);not null;uniqueIndex:idx_event_upload_timing" json:"kind"`
	Platform    string    `gorm:"type:varchar(12);not null;uniqueIndex:idx_event_upload_timing" json:"platform"`
	Outcome     string    `gorm:"type:varchar(8);not null;uniqueIndex:idx_event_upload_timing" json:"outcome"`
	BucketIndex int       `gorm:"not null;uniqueIndex:idx_event_upload_timing" json:"bucket_index"`
	UpperBound  float64   `gorm:"not null" json:"upper_bound"`
	SampleCount int64     `gorm:"not null;default:0" json:"sample_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
