package seeds

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSeedCodeReviewAIActionPolicyIsOptInAndRequiresACompleteSupportedPair(t *testing.T) {
	if err := SeedCodeReviewAIActionPolicy(nil, "", ""); err != nil {
		t.Fatalf("empty bootstrap should be disabled: %v", err)
	}
	for _, test := range []struct {
		provider string
		model    string
	}{
		{provider: "minimax"},
		{model: "MiniMax-M3"},
		{provider: "unknown", model: "model"},
	} {
		if err := SeedCodeReviewAIActionPolicy(nil, test.provider, test.model); err == nil {
			t.Fatalf("invalid bootstrap pair provider=%q model=%q was accepted", test.provider, test.model)
		}
	}
}

func TestSeedCodeReviewAIActionPolicyCreatesPolicyAndImmutableFirstRevision(t *testing.T) {
	db, mock, cleanup := codeReviewPolicySeedDB(t)
	defer cleanup()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO automation_ai_action_policies")).
		WithArgs(sqlmock.AnyArg(), "code.review", "minimax", "MiniMax-M3", `[{"provider":"minimax","model":"MiniMax-M3","reasoning_enabled":false}]`, codeReviewPolicyBootstrapActor, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO automation_ai_action_policy_revisions")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "code.review", `[{"provider":"minimax","model":"MiniMax-M3","reasoning_enabled":false}]`, sqlmock.AnyArg(), codeReviewPolicyBootstrapActor, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := SeedCodeReviewAIActionPolicy(db, " MiniMax ", " MiniMax-M3 "); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSeedCodeReviewAIActionPolicyPreservesExistingOperatorPolicy(t *testing.T) {
	db, mock, cleanup := codeReviewPolicySeedDB(t)
	defer cleanup()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO automation_ai_action_policies")).
		WithArgs(sqlmock.AnyArg(), "code.review", "minimax", "MiniMax-M3", sqlmock.AnyArg(), codeReviewPolicyBootstrapActor, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := SeedCodeReviewAIActionPolicy(db, "minimax", "MiniMax-M3"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func codeReviewPolicySeedDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = sqlDB.Close()
		t.Fatal(err)
	}
	return db, mock, func() { _ = sqlDB.Close() }
}
