//go:build integration

package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutomationInferenceAttemptPoliciesAreAppendOnly(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			rollbackFixture := errors.New("rollback inference attempt policy fixture")
			var mutationErr error
			err := configuration.DB.Transaction(func(tx *gorm.DB) error {
				policy := models.AutomationInferenceAttemptPolicy{
					ID: uuid.Must(uuid.NewV4()), AutomationTaskID: uuid.Must(uuid.NewV4()),
					RunID: uuid.Must(uuid.NewV4()).String(), Operation: "delivery.implementation",
					PolicyRevision: 1, RoutesJSON: `[]`, RoutesHash: strings.Repeat("a", 64),
					MaxCompletionTokens: 1024, SnapshotHash: strings.Repeat("b", 64),
					SignatureKeyID: "integration", SnapshotSignature: strings.Repeat("d", 64), CreatedAt: time.Now().UTC(),
				}
				require.NoError(t, tx.Create(&policy).Error)
				switch operation {
				case "update":
					mutationErr = tx.Model(&models.AutomationInferenceAttemptPolicy{}).Where("id = ?", policy.ID).Update("routes_hash", strings.Repeat("c", 64)).Error
				case "delete":
					mutationErr = tx.Delete(&policy).Error
				}
				return rollbackFixture
			})
			require.ErrorIs(t, err, rollbackFixture, "the fixture transaction must roll back")
			require.Error(t, mutationErr)
			require.Contains(t, strings.ToLower(mutationErr.Error()), "automation inference attempt policies are append-only")
		})
	}
}
