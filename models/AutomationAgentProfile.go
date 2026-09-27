package models

import (
	"encoding/json"
	"time"

	"github.com/gofrs/uuid"
)

// AutomationAgentProfile describes a logical agent specialization. Worker
// processes are represented separately by AutomationAgentHeartbeat so one
// profile can be served by several local machines at once.
type AutomationAgentProfile struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"-"`
	AgentKey         string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	Name             string    `gorm:"type:varchar(120);not null" json:"-"`
	Specialty        string    `gorm:"type:varchar(160);not null;default:''" json:"-"`
	Description      string    `gorm:"type:text;not null;default:''" json:"-"`
	OperationsJSON   string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	CapabilitiesJSON string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	Active           bool      `gorm:"not null;default:true;index" json:"-"`
	CreatedAt        time.Time `json:"-"`
	UpdatedAt        time.Time `json:"-"`
}

// DefaultGeneralistAgentProfile is the bootstrap profile for existing worker
// installations. Its operations mirror the backend allow-list; profile
// seeding uses first-create semantics so operator-edited metadata is retained.
func DefaultGeneralistAgentProfile() AutomationAgentProfile {
	operations, _ := json.Marshal([]string{
		"ai.chat", "document.analyze", "code.review", "product.ideate",
		"delivery.chat", "delivery.plan", "delivery.implementation",
		"delivery.assessment", "delivery.publish", "delivery.qa", "delivery.summary",
	})
	capabilities, _ := json.Marshal([]string{"model_inference", "delivery_orchestration", "stagehand_qa"})
	return AutomationAgentProfile{
		AgentKey:         "generalist",
		Name:             "Generalist",
		Specialty:        "General-purpose ITBEM automation",
		Description:      "Executes allow-listed automation operations across projects.",
		OperationsJSON:   string(operations),
		CapabilitiesJSON: string(capabilities),
		Active:           true,
	}
}
