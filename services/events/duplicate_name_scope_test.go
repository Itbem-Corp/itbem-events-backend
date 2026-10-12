package events

import (
	"errors"
	"regexp"

	"events-stocks/dtos"
	"events-stocks/repositories/eventsrepository"
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
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM "events" WHERE lower(btrim(name)) = lower(btrim($1)) AND client_id = $2 AND "events"."deleted_at" IS NULL`)).
		WithArgs("Boda (copia)", clientID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	name, err := generateUniqueEventName(db, &clientID, "Boda (copia)")
	require.NoError(t, err)
	assert.Equal(t, "Boda (copia)", name)
	require.NoError(t, mock.ExpectationsWereMet())
}

// EVT-22: "boda (copia)" already taken in another letter case pushes the copy
// to the next number instead of creating a case-only twin.
func TestGenerateUniqueEventNameIgnoresLetterCase(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	clientID := uuid.Must(uuid.NewV4())
	countSQL := regexp.QuoteMeta(`SELECT count(*) FROM "events" WHERE lower(btrim(name)) = lower(btrim($1)) AND client_id = $2`)
	mock.ExpectQuery(countSQL).WithArgs("Boda (copia)", clientID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(countSQL).WithArgs("Boda (copia) 2", clientID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	name, err := generateUniqueEventName(db, &clientID, "Boda (copia)")
	require.NoError(t, err)
	assert.Equal(t, "Boda (copia) 2", name)
	require.NoError(t, mock.ExpectationsWereMet())
}

// EVT-87: a name typed by the organizer that is already taken is a conflict
// (409 event_name_taken), never a silent rename to "<name> 2".
func TestDuplicateEventWithTakenRequestedNameIsAConflict(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)

	sourceID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "events" WHERE id = $1`)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "client_id", "name", "timezone"}).AddRow(sourceID, clientID, "Boda", "America/Mexico_City"))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM "events" WHERE lower(btrim(name)) = lower(btrim($1)) AND client_id = $2`)).
		WithArgs("QA events 2", clientID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectRollback()

	name := "QA events 2"
	svc := NewDuplicateService(db, DuplicateServiceDeps{})
	_, err = svc.DuplicateEventByID(sourceID, dtos.EventPayload{Name: &name})
	require.Error(t, err)
	assert.True(t, errors.Is(err, eventsrepository.ErrEventNameAlreadyExists))
	require.NoError(t, mock.ExpectationsWereMet())
}
