//go:build integration

package eventsrepository

// Real-PostgreSQL tests for two boot migrations: the timezone repair
// (backend review M4/M7.3) and the moment dedupe order (M7.1). They share the
// disposable container of postgres_integration_test.go; nothing touches the
// local stack database.

import (
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"events-stocks/configuration"
	"events-stocks/models"
)

// Review M4: the boot repair blanks only zones PostgreSQL rejects; values it
// accepts (lower-case IANA, POSIX) are kept, and a second boot is a no-op.
func TestStartupTimezoneRepairKeepsZonesPostgresAccepts(t *testing.T) {
	suffix := lifecycleSuffix()
	org := createLifecycleClient(t, suffix, "TZK")
	bad := createLifecycleEvent(t, &org, "Mala "+suffix, "tz-mala-"+suffix)
	lower := createLifecycleEvent(t, &org, "Minúsculas "+suffix, "tz-lower-"+suffix)
	posix := createLifecycleEvent(t, &org, "Posix "+suffix, "tz-posix-"+suffix)
	require.NoError(t, configuration.DB.Exec(`UPDATE events SET timezone = 'Mars/Olympus' WHERE id = ?`, bad.ID).Error)
	require.NoError(t, configuration.DB.Exec(`UPDATE events SET timezone = 'america/mexico_city' WHERE id = ?`, lower.ID).Error)
	require.NoError(t, configuration.DB.Exec(`UPDATE events SET timezone = 'UTC+5' WHERE id = ?`, posix.ID).Error)

	require.NoError(t, configuration.MigrateModelsForTest(configuration.DB))
	require.NoError(t, configuration.MigrateModelsForTest(configuration.DB), "idempotent")

	zone := func(id uuid.UUID) string {
		var value string
		require.NoError(t, configuration.DB.Raw(`SELECT timezone FROM events WHERE id = ?`, id).Row().Scan(&value))
		return value
	}
	assert.Equal(t, "", zone(bad.ID))
	assert.Equal(t, "america/mexico_city", zone(lower.ID))
	assert.Equal(t, "UTC+5", zone(posix.ID))
}

// Review M7: the dedupe keeps the approved copy even when another copy has a
// legacy NULL is_approved (plain DESC sorts NULL first).
func TestMomentDedupeKeepsApprovedCopyOverLegacyNull(t *testing.T) {
	suffix := lifecycleSuffix()
	org := createLifecycleClient(t, suffix, "MOMD")
	event := createLifecycleEvent(t, &org, "Momentos "+suffix, "momentos-"+suffix)
	db := configuration.DB
	t.Cleanup(func() { db.Unscoped().Where("event_id = ?", event.ID).Delete(&models.Moment{}) })
	require.NoError(t, db.Exec("DROP INDEX IF EXISTS "+models.MomentLiveContentURLIndex).Error)

	key := "moments/" + event.ID.String() + "/raw/" + uuid.Must(uuid.NewV4()).String() + ".jpg"
	legacy := models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &event.ID, ContentURL: key, ProcessingStatus: "done", CreatedAt: time.Now().Add(-2 * time.Hour)}
	approved := models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &event.ID, ContentURL: key, ProcessingStatus: "done", IsApproved: true}
	for _, moment := range []*models.Moment{&legacy, &approved} {
		require.NoError(t, db.Omit("Invitation", "MomentType", "Guest").Create(moment).Error)
	}
	require.NoError(t, db.Exec(`ALTER TABLE moments ALTER COLUMN is_approved DROP NOT NULL`).Error)
	require.NoError(t, db.Exec(`UPDATE moments SET is_approved = NULL WHERE id = ?`, legacy.ID).Error)

	require.NoError(t, configuration.MigrateModelsForTest(db))

	var live []models.Moment
	require.NoError(t, db.Where("event_id = ? AND content_url = ?", event.ID, key).Find(&live).Error)
	require.Len(t, live, 1)
	assert.Equal(t, approved.ID, live[0].ID, "the approved copy must survive")
}
