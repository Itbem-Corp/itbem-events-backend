//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
)

func TestAutomationProviderUsageSnapshotsAreAppendOnlyAtDatabaseBoundary(t *testing.T) {
	snapshot := models.AutomationProviderUsageSnapshot{
		ID: uuid.Must(uuid.NewV4()), CaptureID: uuid.Must(uuid.NewV4()), ProjectID: uuid.Must(uuid.NewV4()),
		Provider: "deepseek", Status: "available", BillingModel: "token", CredentialScope: "project",
		ObservedAt: time.Now().UTC(), Currency: "USD", WindowsJSON: `[]`, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, configuration.DB.Create(&snapshot).Error)

	updateResult := configuration.DB.Exec(
		"UPDATE automation_provider_usage_snapshots SET status = ? WHERE id = ?",
		"error", snapshot.ID,
	)
	require.Error(t, updateResult.Error)
	require.Contains(t, strings.ToLower(updateResult.Error.Error()), "automation provider usage snapshots are append-only")

	deleteResult := configuration.DB.Exec(
		"DELETE FROM automation_provider_usage_snapshots WHERE id = ?", snapshot.ID,
	)
	require.Error(t, deleteResult.Error)
	require.Contains(t, strings.ToLower(deleteResult.Error.Error()), "automation provider usage snapshots are append-only")
}
