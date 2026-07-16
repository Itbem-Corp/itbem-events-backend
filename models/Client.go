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

	Logo        string `gorm:"text" json:"logo"`
	MediaBucket string `gorm:"type:varchar(255);not null;default:'';index" json:"-"`
	IsActive    bool   `gorm:"default:true" json:"is_active"`
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

// AfterCreate keeps application access in the same transaction as the
// organization membership. It resolves the nearest first-party portal in the
// hierarchy, so a Cafetton membership does not silently become an EventiApp
// platform membership.
func (member *ClientMember) AfterCreate(tx *gorm.DB) error {
	return tx.Exec(`
		WITH RECURSIVE ancestry AS (
			SELECT id, parent_id, code, 0 AS depth
			FROM clients
			WHERE id = ? AND deleted_at IS NULL
			UNION ALL
			SELECT parent.id, parent.parent_id, parent.code, ancestry.depth + 1
			FROM ancestry
			JOIN clients parent ON parent.id = ancestry.parent_id
			WHERE parent.deleted_at IS NULL AND ancestry.depth < 31
		),
		resolved AS (
			SELECT applications.id
			FROM ancestry
			JOIN applications ON LOWER(applications.code) = LOWER(ancestry.code)
			WHERE applications.is_active = true
			ORDER BY ancestry.depth ASC
			LIMIT 1
		)
		INSERT INTO client_member_applications
			(id, client_member_id, application_id, is_active, created_at, updated_at)
		SELECT uuid_generate_v4(), ?, resolved.id, true, NOW(), NOW()
		FROM resolved
		ON CONFLICT (client_member_id, application_id)
		DO UPDATE SET is_active = true, updated_at = NOW()
	`, member.ClientID, member.ID).Error
}
