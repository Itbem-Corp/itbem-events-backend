package events

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// EVT-2: the duplicate's name only has to be free inside its organization
// (idx_events_client_name_live), so another tenant's "X (copia)" must not push
// the copy to "X (copia) 2" (and must not be observable from here).
func TestGenerateUniqueEventNameChecksOnlyTheSameOrganization(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	clientID := uuid.Must(uuid.NewV4())
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM "events" WHERE name = $1 AND client_id = $2 AND "events"."deleted_at" IS NULL`)).
		WithArgs("Boda (copia)", clientID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	name, err := generateUniqueEventName(db, &clientID, "Boda (copia)")
	require.NoError(t, err)
	assert.Equal(t, "Boda (copia)", name)
	require.NoError(t, mock.ExpectationsWereMet())
}
