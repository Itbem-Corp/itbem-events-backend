package eventsectionrepository

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"
	"events-stocks/services/ports"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// STU-N3: the conditional write compares updated_at at microsecond precision
// in the UPDATE itself and reports zero affected rows as a conflict.
func TestUpdateEventSectionIfUnmodified(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() { configuration.DB = previous })

	section := &models.EventSection{ID: uuid.Must(uuid.NewV4()), Config: `{}`}
	expected := time.Date(2026, 10, 12, 10, 0, 5, 123456789, time.UTC)
	truncated := time.Date(2026, 10, 12, 10, 0, 5, 123456000, time.UTC)

	for _, tc := range []struct {
		rows    int64
		wantErr error
	}{{1, nil}, {0, ports.ErrEventSectionConflict}} {
		mock.ExpectBegin()
		mock.ExpectExec(`UPDATE "event_sections" SET .* WHERE \(id = \$\d+ AND updated_at = \$\d+\)`).
			WithArgs(append(anyArgs(10), section.ID, truncated, sqlmock.AnyArg())...).
			WillReturnResult(sqlmock.NewResult(0, tc.rows))
		mock.ExpectCommit()
		err := UpdateEventSectionIfUnmodified(section, expected)
		if !errors.Is(err, tc.wantErr) {
			t.Fatalf("rows=%d: got %v, want %v", tc.rows, err, tc.wantErr)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func anyArgs(n int) []driver.Value {
	args := make([]driver.Value, n)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	return args
}
