package gormrepository

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"events-stocks/configuration"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type updateTestParent struct {
	ID   string `gorm:"primaryKey"`
	Name string
}

type updateTestRow struct {
	ID       string `gorm:"primaryKey"`
	Name     string
	ParentID *string
	Parent   *updateTestParent `gorm:"foreignKey:ParentID"`
}

func withMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := configuration.DB
	configuration.DB = db
	t.Cleanup(func() {
		configuration.DB = previous
		_ = sqlDB.Close()
	})
	return mock
}

func TestUpdateReturnsDatabaseErrorInsteadOfRecordNotFound(t *testing.T) {
	mock := withMockDB(t)
	dbErr := errors.New(`ERROR: duplicate key value violates unique constraint "idx_events_identifier_live" (SQLSTATE 23505)`)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "update_test_rows"`)).WillReturnError(dbErr)
	mock.ExpectRollback()

	err := Update(&updateTestRow{ID: "a", Name: "x"}, "a")
	if err == nil || !strings.Contains(err.Error(), "idx_events_identifier_live") {
		t.Fatalf("expected the database error, got %v", err)
	}
}

func TestUpdateStillReportsRecordNotFoundWhenNoRowMatches(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "update_test_rows"`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err := Update(&updateTestRow{ID: "a", Name: "x"}, "a")
	if err == nil || err.Error() != "record not found" {
		t.Fatalf("expected record not found, got %v", err)
	}
}

func TestUpdateFieldsByIDReturnsDatabaseErrorFirst(t *testing.T) {
	mock := withMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "update_test_rows"`)).WillReturnError(errors.New("boom"))
	mock.ExpectRollback()

	err := UpdateFieldsByID("a", map[string]interface{}{"name": "x"}, &updateTestRow{})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom, got %v", err)
	}
}

// A loaded belongs-to association must not rewrite the foreign key that the
// caller changed by ID: the UPDATE must carry the new parent_id and must not
// upsert the stale parent row first.
func TestUpdateOmitAssociationsKeepsChangedForeignKey(t *testing.T) {
	mock := withMockDB(t)
	newParent := "new-parent"
	row := updateTestRow{ID: "a", Name: "x", ParentID: &newParent, Parent: &updateTestParent{ID: "old-parent", Name: "old"}}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "update_test_rows" SET "name"=$1,"parent_id"=$2 WHERE id = $3 AND "id" = $4`)).
		WithArgs("x", "new-parent", "a", "a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := UpdateOmitAssociations(&row, "a"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if row.ParentID == nil || *row.ParentID != "new-parent" {
		t.Fatalf("foreign key was rewritten from the loaded association: %v", row.ParentID)
	}
}
