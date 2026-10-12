package seeds

import (
	"os"
	"strings"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestBrandRootApplicationUpsertNeverReenablesAccess(t *testing.T) {
	assert.ElementsMatch(t, []string{"modules", "updated_at"}, brandRootApplicationUpdateColumns)
	assert.NotContains(t, brandRootApplicationUpdateColumns, "is_active")
}

func TestMemberApplicationBackfillOnlyInsertsMissingRows(t *testing.T) {
	normalized := strings.Join(strings.Fields(memberApplicationBackfillSQL), " ")
	assert.Contains(t, normalized, "ON CONFLICT (client_member_id, application_id) DO NOTHING")
	assert.NotContains(t, normalized, "DO UPDATE")
	assert.NotContains(t, normalized, "is_active = true, updated_at")
}

// Opt-in: proves on a real Postgres that a disabled brand-root product and a
// disabled member entitlement stay disabled after the boot seed. The outer
// transaction always rolls back.
func TestSeedApplicationsPostgresKeepsDisabledAccessDisabled(t *testing.T) {
	dsn := os.Getenv("APPLICATION_SEED_TEST_DSN")
	if dsn == "" {
		t.Skip("APPLICATION_SEED_TEST_DSN is not configured")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })

	var memberAccess models.ClientMemberApplication
	if err := tx.Order("created_at ASC").First(&memberAccess).Error; err != nil {
		t.Skip("no client_member_applications row to exercise")
	}
	var rootAccess models.ClientApplication
	require.NoError(t, tx.Raw(`
		SELECT client_applications.* FROM client_applications
		JOIN clients ON clients.id = client_applications.client_id
		JOIN applications ON applications.id = client_applications.application_id
		WHERE LOWER(clients.code) = LOWER(applications.code)
		ORDER BY client_applications.created_at ASC LIMIT 1`).Scan(&rootAccess).Error)
	if rootAccess.ID == uuid.Nil {
		t.Skip("no brand-root client_applications row to exercise")
	}

	require.NoError(t, tx.Model(&models.ClientMemberApplication{}).Where("id = ?", memberAccess.ID).Update("is_active", false).Error)
	require.NoError(t, tx.Model(&models.ClientApplication{}).Where("id = ?", rootAccess.ID).Update("is_active", false).Error)

	require.NoError(t, SeedApplications(tx))

	var memberActive, rootActive bool
	require.NoError(t, tx.Raw("SELECT is_active FROM client_member_applications WHERE id = ?", memberAccess.ID).Scan(&memberActive).Error)
	require.NoError(t, tx.Raw("SELECT is_active FROM client_applications WHERE id = ?", rootAccess.ID).Scan(&rootActive).Error)
	assert.False(t, memberActive, "member backfill must not re-enable a disabled entitlement")
	assert.False(t, rootActive, "brand-root upsert must not re-enable a disabled product")
}
