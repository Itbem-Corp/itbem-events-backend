package automation

import (
	"context"
	"errors"
	"testing"

	"events-stocks/configuration"
	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAICredentialBundleWriteLockRunsMutationInsidePostgresTransaction(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).
		WithArgs(aiCredentialBundleAdvisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	called := false
	err = withAICredentialBundleWriteLock(context.Background(), func() error {
		called = true
		return nil
	})
	if err != nil || !called {
		t.Fatalf("bundle mutation called=%v err=%v", called, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAICredentialBundleWriteLockFailsClosedBeforeMutation(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := configuration.DB
	configuration.DB = db
	defer func() { configuration.DB = previousDB }()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).
		WithArgs(aiCredentialBundleAdvisoryLockKey).
		WillReturnError(errors.New("database lock unavailable"))
	mock.ExpectRollback()
	called := false
	err = withAICredentialBundleWriteLock(context.Background(), func() error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("unlocked bundle mutation called=%v err=%v", called, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
