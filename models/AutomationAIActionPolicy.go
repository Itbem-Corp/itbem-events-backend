package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const MaxAutomationAIActionRoutes = 3

// AutomationAIActionRoute is one operator-selected candidate in an ordered
// inference route. It contains routing data only; credentials remain in the
// environment-scoped provider credential bundle.
type AutomationAIActionRoute struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	ReasoningEnabled bool   `json:"reasoning_enabled"`
	ReasoningEffort  string `json:"reasoning_effort,omitempty"`
}

// AutomationAIActionPolicy is the platform-owned model routing decision for a
// single bounded automation operation. It deliberately contains no credential:
// keys stay in the separate environment credential bundle.
type AutomationAIActionPolicy struct {
	ID               uuid.UUID `gorm:"type:uuid;default:uuid_generate_v4();primaryKey" json:"id"`
	Operation        string    `gorm:"type:varchar(96);not null;uniqueIndex" json:"operation"`
	Provider         string    `gorm:"type:varchar(48);not null" json:"provider"`
	Model            string    `gorm:"type:varchar(200);not null" json:"model"`
	ReasoningEnabled bool      `gorm:"not null;default:false" json:"reasoning_enabled"`
	ReasoningEffort  string    `gorm:"type:varchar(16);not null;default:''" json:"reasoning_effort,omitempty"`
	// RoutesJSON stores up to three ordered candidates. Provider/Model remain
	// populated with the primary route for safe backwards compatibility with
	// historical rows and views while the migration rolls out.
	RoutesJSON string    `gorm:"type:jsonb;not null;default:'[]'" json:"-"`
	Revision   int64     `gorm:"not null;default:0" json:"revision"`
	UpdatedBy  string    `gorm:"type:varchar(128);not null;default:''" json:"updated_by,omitempty"`
	CreatedAt  time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt  time.Time `gorm:"not null" json:"updated_at"`
}

func (p AutomationAIActionPolicy) Routes() ([]AutomationAIActionRoute, error) {
	raw := strings.TrimSpace(p.RoutesJSON)
	if raw == "" || raw == "[]" {
		if strings.TrimSpace(p.Provider) == "" || strings.TrimSpace(p.Model) == "" {
			return nil, fmt.Errorf("automation action policy has no route")
		}
		return []AutomationAIActionRoute{{Provider: p.Provider, Model: p.Model, ReasoningEnabled: p.ReasoningEnabled, ReasoningEffort: p.ReasoningEffort}}, nil
	}
	var routes []AutomationAIActionRoute
	if err := json.Unmarshal([]byte(raw), &routes); err != nil || len(routes) == 0 || len(routes) > MaxAutomationAIActionRoutes {
		return nil, fmt.Errorf("automation action policy routes are invalid")
	}
	for _, route := range routes {
		if strings.TrimSpace(route.Provider) == "" || strings.TrimSpace(route.Model) == "" {
			return nil, fmt.Errorf("automation action policy routes are invalid")
		}
	}
	return routes, nil
}
