package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationAgentInstance is an enrolled local machine for one logical agent
// profile. Only a public Ed25519 key is stored in the cloud; the corresponding
// private key remains in the machine's protected local identity store.
type AutomationAgentInstance struct {
	ID                   uuid.UUID  `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	AgentKey             string     `gorm:"type:varchar(64);not null;index;uniqueIndex:idx_agent_instance_active_machine,priority:1,where:status = 'active'" json:"-"`
	MachineID            string     `gorm:"type:varchar(64);not null;uniqueIndex:idx_agent_instance_active_machine,priority:2,where:status = 'active'" json:"-"`
	PublicKey            string     `gorm:"type:varchar(64);not null" json:"-"`
	PublicKeyFingerprint string     `gorm:"type:varchar(80);not null" json:"-"`
	Status               string     `gorm:"type:varchar(16);not null;default:'active';index;uniqueIndex:idx_agent_instance_active_machine,priority:3,where:status = 'active'" json:"-"`
	LastSeenAt           *time.Time `gorm:"index" json:"-"`
	RevokedAt            *time.Time `json:"-"`
	CreatedAt            time.Time  `json:"-"`
	UpdatedAt            time.Time  `json:"-"`
}

// AutomationAgentCallbackNonce is a short-lived durable replay guard. Its
// unique constraint works across all API replicas, unlike an in-memory cache.
type AutomationAgentCallbackNonce struct {
	ID         uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	InstanceID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_agent_callback_instance_nonce,priority:1;index:idx_agent_callback_nonce_created" json:"-"`
	Nonce      string    `gorm:"type:uuid;not null;uniqueIndex:idx_agent_callback_instance_nonce,priority:2" json:"-"`
	CreatedAt  time.Time `gorm:"not null;index:idx_agent_callback_nonce_created" json:"-"`
}
