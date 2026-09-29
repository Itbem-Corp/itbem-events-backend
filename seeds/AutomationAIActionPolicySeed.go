package seeds

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const codeReviewPolicyBootstrapActor = "system:bootstrap-budget-route"

// SeedCodeReviewAIActionPolicy creates the first code.review routing policy
// from the server-owned budget provider/model pair. It is deliberately
// create-only: once an operator has configured the operation, startup never
// edits that policy or appends another revision.
func SeedCodeReviewAIActionPolicy(db *gorm.DB, configuredProvider, configuredModel string) error {
	provider := strings.ToLower(strings.TrimSpace(configuredProvider))
	model := strings.TrimSpace(configuredModel)
	if provider == "" && model == "" {
		return nil
	}
	if db == nil || provider == "" || model == "" || len(provider) > 48 || len(model) > 200 {
		return errors.New("code review AI policy bootstrap configuration is incomplete")
	}
	if _, supported := automationagent.DefaultProviderEndpoint(automationagent.Provider(provider)); !supported {
		return errors.New("code review AI policy bootstrap provider is unsupported")
	}

	routesJSON, err := json.Marshal([]models.AutomationAIActionRoute{{Provider: provider, Model: model}})
	if err != nil {
		return fmt.Errorf("encode code review AI policy bootstrap: %w", err)
	}
	routesHash := fmt.Sprintf("%x", sha256.Sum256(routesJSON))
	now := time.Now().UTC()
	policyID, revisionID := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4())

	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`INSERT INTO automation_ai_action_policies
			(id, operation, provider, model, reasoning_enabled, reasoning_effort, routes_json, revision, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, FALSE, '', CAST(? AS jsonb), 1, ?, ?, ?)
			ON CONFLICT (operation) DO NOTHING`,
			policyID, "code.review", provider, model, string(routesJSON), codeReviewPolicyBootstrapActor, now, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if result.RowsAffected != 1 {
			return errors.New("code review AI policy bootstrap wrote an unexpected row count")
		}
		return tx.Exec(`INSERT INTO automation_ai_action_policy_revisions
			(id, policy_id, operation, revision, routes_json, routes_hash, changed_by, created_at)
			VALUES (?, ?, ?, 1, CAST(? AS jsonb), ?, ?, ?)`,
			revisionID, policyID, "code.review", string(routesJSON), routesHash, codeReviewPolicyBootstrapActor, now).Error
	})
}
