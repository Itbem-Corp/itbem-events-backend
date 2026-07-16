package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// ProductMetricDaily stores compact operational telemetry by product and
// organization. It intentionally contains aggregates only: no paths, IPs,
// user agents, tokens, or customer content.
type ProductMetricDaily struct {
	Day          time.Time `gorm:"type:date;primaryKey" json:"day"`
	TenantCode   string    `gorm:"type:varchar(32);primaryKey" json:"tenant_code"`
	ClientID     uuid.UUID `gorm:"type:uuid;primaryKey;index" json:"client_id"`
	Requests     int64     `gorm:"not null;default:0" json:"requests"`
	Mutations    int64     `gorm:"not null;default:0" json:"mutations"`
	Errors       int64     `gorm:"not null;default:0" json:"errors"`
	DurationMS   int64     `gorm:"not null;default:0" json:"duration_ms"`
	RequestBytes int64     `gorm:"not null;default:0" json:"request_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (ProductMetricDaily) TableName() string { return "product_metric_daily" }

// ProductActiveUserDaily makes daily active-user counts exact while keeping
// cardinality bounded to one row per authenticated user/product/org/day.
type ProductActiveUserDaily struct {
	Day        time.Time `gorm:"type:date;primaryKey" json:"day"`
	TenantCode string    `gorm:"type:varchar(32);primaryKey" json:"tenant_code"`
	ClientID   uuid.UUID `gorm:"type:uuid;primaryKey;index" json:"client_id"`
	UserID     uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (ProductActiveUserDaily) TableName() string { return "product_active_user_daily" }
