package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventPerformanceDaily stores aggregate-only RUM samples. No guest, IP,
// token, user-agent, or raw URL is persisted.
type EventPerformanceDaily struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_event_perf_bucket" json:"event_id"`
	BucketDate  time.Time `gorm:"type:date;not null;uniqueIndex:idx_event_perf_bucket" json:"bucket_date"`
	Route       string    `gorm:"type:varchar(20);not null;uniqueIndex:idx_event_perf_bucket" json:"route"`
	Metric      string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_event_perf_bucket" json:"metric"`
	SampleCount int64     `gorm:"not null;default:0" json:"sample_count"`
	ValueSum    float64   `gorm:"not null;default:0" json:"value_sum"`
	ValueMin    float64   `gorm:"not null;default:0" json:"value_min"`
	ValueMax    float64   `gorm:"not null;default:0" json:"value_max"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
