package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationProviderModelSnapshot is an audit-only, credential-free record of
// a provider catalogue revision. Rows are appended only when its content hash
// changes, keeping model removal/price/capability changes reviewable.
type AutomationProviderModelSnapshot struct {
	ID          uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Provider    string    `gorm:"type:varchar(48);not null;index" json:"provider"`
	CatalogHash string    `gorm:"type:char(64);not null;uniqueIndex" json:"catalog_hash"`
	ModelCount  int       `gorm:"not null" json:"model_count"`
	CatalogJSON string    `gorm:"type:jsonb;not null" json:"-"`
	CapturedAt  time.Time `gorm:"not null;index" json:"captured_at"`
	CreatedAt   time.Time `gorm:"not null" json:"created_at"`
}
