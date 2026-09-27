package models

import (
	"time"

	"github.com/gofrs/uuid"
)

// AutomationAgentHeartbeat is a short-lived operational presence record. It
// contains no host name, queue URL, prompt, result, token or credential. The
// workspace readiness JSON is a deliberately small projection (booleans and
// command counts only) so the dashboard can distinguish a live worker from a
// worker that is actually prepared to execute Delivery work.
type AutomationAgentHeartbeat struct {
	ID       uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	WorkerID string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"worker_id"`
	AgentKey string    `gorm:"type:varchar(64);not null;default:'generalist';index" json:"agent_key"`
	// MachineID is an operator-provided opaque identifier. It must never contain
	// a hostname or local path; empty means that the worker did not configure it.
	MachineID string `gorm:"type:varchar(64);not null;default:''" json:"-"`
	// AgentInstanceID is populated only from the authenticated callback context.
	// It is nullable for historical heartbeats recorded before machine enrollment.
	AgentInstanceID  *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	Provider         string     `gorm:"type:varchar(48);not null;default:''" json:"provider,omitempty"`
	Model            string     `gorm:"type:varchar(128);not null;default:''" json:"model,omitempty"`
	Concurrency      int        `gorm:"not null;default:1" json:"concurrency"`
	Draining         bool       `gorm:"not null;default:false;index" json:"draining"`
	CapabilitiesJSON string     `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	// ProtocolsJSON records the explicit callback/execution contracts supported
	// by this worker. An empty list is legacy-compatible and is not evidence of
	// Delivery plan-step protocol support.
	ProtocolsJSON      string    `gorm:"column:protocols_json;type:jsonb;not null;default:'[]'" json:"-"`
	WorkspaceReadiness string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	StartedAt          time.Time `gorm:"not null" json:"started_at"`
	LastSeenAt         time.Time `gorm:"not null;index" json:"last_seen_at"`
	CreatedAt          time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt          time.Time `gorm:"not null" json:"updated_at"`
}
