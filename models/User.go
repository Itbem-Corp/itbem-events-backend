package models

import (
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
	"time"
)

type User struct {
	ID                 uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	CognitoSub         string    `gorm:"uniqueIndex;not null" json:"-"`
	Email              string    `gorm:"index" json:"email"`
	FirstName          string    `json:"first_name"`
	LastName           string    `json:"last_name"`
	ProfileImage       string    `gorm:"text" json:"profile_image"`
	ProfileImageBucket string    `gorm:"type:varchar(255);not null;default:'';index" json:"-"`
	IsActive           bool      `gorm:"default:true" json:"is_active"`
	// RootLevel separates primary platform administrators from constrained
	// operational administrators. 0 = none, 1 = primary, 2 = operational.
	// Lower numbers have more authority.
	RootLevel      int    `gorm:"default:0;index" json:"root_level"`
	IsRoot         bool   `gorm:"default:false" json:"is_root"`
	AuthTenantCode string `gorm:"-" json:"-"`
	// Relaciones (Has Many)
	EventMembers  []EventMember  `gorm:"foreignKey:UserID" json:"event_members,omitempty"`
	ClientMembers []ClientMember `gorm:"foreignKey:UserID" json:"client_members,omitempty"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

const (
	RootLevelNone        = 0
	RootLevelPrimary     = 1
	RootLevelOperational = 2
)

// EffectiveRootLevel keeps historical IsRoot=true accounts as primary
// administrators until they are explicitly assigned a level.
func (u *User) EffectiveRootLevel() int {
	if u == nil {
		return RootLevelNone
	}
	if u.RootLevel > RootLevelNone {
		return u.RootLevel
	}
	if u.IsRoot {
		return RootLevelPrimary
	}
	return RootLevelNone
}

func (u *User) IsPlatformAdmin() bool { return u.EffectiveRootLevel() > RootLevelNone }

func (u *User) IsPrimaryRoot() bool { return u.EffectiveRootLevel() == RootLevelPrimary }
