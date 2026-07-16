package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid"
)

// StringList is stored as JSONB so application capabilities remain
// data-driven without coupling the database to dashboard route names.
type StringList []string

func (list StringList) Value() (driver.Value, error) {
	if list == nil {
		list = StringList{}
	}
	return json.Marshal(list)
}

func (list *StringList) Scan(value interface{}) error {
	if value == nil {
		*list = StringList{}
		return nil
	}
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		return fmt.Errorf("unsupported StringList value %T", value)
	}
	return json.Unmarshal(raw, list)
}

// Application describes a product/portal boundary. Its code intentionally
// matches the tenant_code resolved from the Cognito audience and API host.
type Application struct {
	ID                  uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Code                string     `gorm:"uniqueIndex;not null" json:"code"`
	Name                string     `gorm:"not null" json:"name"`
	ProductLabel        string     `gorm:"not null;default:''" json:"product_label"`
	Modules             StringList `gorm:"type:jsonb;not null;default:'[]'" json:"modules"`
	AllowsPlatformAdmin bool       `gorm:"not null;default:false" json:"allows_platform_admin"`
	IsActive            bool       `gorm:"not null;default:true" json:"is_active"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// ClientApplication enables an application for an organization. The first
// party roots are seeded, while customer organizations may be enabled later by
// provisioning/billing flows.
type ClientApplication struct {
	ID            uuid.UUID   `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ClientID      uuid.UUID   `gorm:"type:uuid;not null;uniqueIndex:idx_client_application" json:"client_id"`
	Client        Client      `gorm:"foreignKey:ClientID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ApplicationID uuid.UUID   `gorm:"type:uuid;not null;uniqueIndex:idx_client_application" json:"application_id"`
	Application   Application `gorm:"foreignKey:ApplicationID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	Modules       StringList  `gorm:"type:jsonb;not null;default:'[]'" json:"modules"`
	IsActive      bool        `gorm:"not null;default:true" json:"is_active"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// ClientMemberApplication is the explicit entitlement that lets a membership
// enter an application. Organization roles remain in ClientMember, avoiding a
// second and eventually inconsistent role system.
type ClientMemberApplication struct {
	ID             uuid.UUID    `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	ClientMemberID uuid.UUID    `gorm:"type:uuid;not null;uniqueIndex:idx_member_application" json:"client_member_id"`
	ClientMember   ClientMember `gorm:"foreignKey:ClientMemberID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	ApplicationID  uuid.UUID    `gorm:"type:uuid;not null;uniqueIndex:idx_member_application" json:"application_id"`
	Application    Application  `gorm:"foreignKey:ApplicationID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"-"`
	IsActive       bool         `gorm:"not null;default:true" json:"is_active"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}
