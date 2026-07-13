package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type Client struct {
	ID   uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Name string    `gorm:"not null" json:"name"`
	Code string    `gorm:"uniqueIndex;not null" json:"code"`

	ClientTypeID uuid.UUID  `gorm:"type:uuid;index;not null" json:"client_type_id"`
	ClientType   ClientType `gorm:"foreignKey:ClientTypeID" json:"client_type,omitempty"`

	Logo     string `gorm:"text" json:"logo"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
	// AccessRole is a read-only projection for the current session. It is
	// populated by scoped list queries and is never persisted on clients.
	AccessRole string `gorm:"-" json:"access_role,omitempty"`

	ParentID *uuid.UUID `gorm:"type:uuid;index" json:"parent_id,omitempty"`
	Parent   *Client    `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
	Children []Client   `gorm:"foreignKey:ParentID" json:"children,omitempty"`

	Members []ClientMember `gorm:"foreignKey:ClientID" json:"members,omitempty"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// ClientMember es la tabla intermedia (El Link User <-> Client)
type ClientMember struct {
	ID uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`

	ClientID uuid.UUID `gorm:"type:uuid;index;not null" json:"client_id"`
	Client   Client    `gorm:"foreignKey:ClientID" json:"client,omitempty"`

	UserID uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	User   User      `gorm:"foreignKey:UserID" json:"user,omitempty"`

	ClientRoleID uuid.UUID  `gorm:"type:uuid;index;not null" json:"client_role_id"`
	ClientRole   ClientRole `gorm:"foreignKey:ClientRoleID" json:"client_role,omitempty"`
	IsActive     bool       `gorm:"default:true" json:"is_active"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
