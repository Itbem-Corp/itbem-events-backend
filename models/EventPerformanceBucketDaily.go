package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// EventPerformanceBucketDaily stores a fixed histogram bucket. The bounded
// cardinality makes percentile aggregation inexpensive without retaining raw
// guest samples or identifiers.
type EventPerformanceBucketDaily struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	EventID     uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_event_perf_histogram" json:"event_id"`
	BucketDate  time.Time `gorm:"type:date;not null;uniqueIndex:idx_event_perf_histogram" json:"bucket_date"`
	Route       string    `gorm:"type:varchar(20);not null;uniqueIndex:idx_event_perf_histogram" json:"route"`
	Metric      string    `gorm:"type:varchar(32);not null;uniqueIndex:idx_event_perf_histogram" json:"metric"`
	BucketIndex int       `gorm:"not null;uniqueIndex:idx_event_perf_histogram" json:"bucket_index"`
	UpperBound  float64   `gorm:"not null" json:"upper_bound"`
	SampleCount int64     `gorm:"not null;default:0" json:"sample_count"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
