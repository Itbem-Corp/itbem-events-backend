package momentrepository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMockMomentRepositoryDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()

	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return db, mock
}

func bulkUpdateOrderStatementPattern() string {
	parts := strings.Fields(bulkUpdateOrderSQL)
	pattern := regexp.QuoteMeta(strings.Join(parts, " "))
	pattern = strings.Replace(pattern, regexp.QuoteMeta("?"), `\$1`, 1)
	return `\s*` + strings.ReplaceAll(pattern, " ", `\s+`) + `\s*`
}

func TestBulkUpdateOrderUsesOneParameterizedStatementWithDeterministicPairs(t *testing.T) {
	db, mock := newMockMomentRepositoryDB(t)
	firstID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000001"))
	secondID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000002"))
	updates := map[uuid.UUID]int{
		secondID: -4,
		firstID:  27,
	}
	expectedPayload := `[{"id":"00000000-0000-0000-0000-000000000001","new_order":27},{"id":"00000000-0000-0000-0000-000000000002","new_order":-4}]`

	mock.ExpectBegin()
	mock.ExpectExec(bulkUpdateOrderStatementPattern()).
		WithArgs(expectedPayload).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	require.NoError(t, bulkUpdateOrder(db, updates))
	require.NoError(t, mock.ExpectationsWereMet(), "the repository must issue exactly one batch UPDATE")
}

func TestBulkUpdateOrderStatementPreservesTimestampsAndSoftDeleteScope(t *testing.T) {
	normalizedSQL := strings.ToLower(strings.Join(strings.Fields(bulkUpdateOrderSQL), " "))

	assert.Contains(t, normalizedSQL, `update moments as moment`)
	assert.Contains(t, normalizedSQL, `set "order" = requested.new_order`)
	assert.Contains(t, normalizedSQL, `updated_at = current_timestamp`)
	assert.Contains(t, normalizedSQL, `from jsonb_to_recordset(?::jsonb)`)
	assert.Contains(t, normalizedSQL, `new_order bigint`)
	assert.Contains(t, normalizedSQL, `moment.id = requested.id`)
	assert.Contains(t, normalizedSQL, `moment.deleted_at is null`)
	assert.NotContains(t, normalizedSQL, ";")
}

func TestBulkUpdateOrderRollsBackAndReturnsStatementError(t *testing.T) {
	db, mock := newMockMomentRepositoryDB(t)
	id := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000003"))
	databaseErr := errors.New("postgres update failed")

	mock.ExpectBegin()
	mock.ExpectExec(bulkUpdateOrderStatementPattern()).
		WithArgs(`[{"id":"00000000-0000-0000-0000-000000000003","new_order":8}]`).
		WillReturnError(databaseErr)
	mock.ExpectRollback()

	err := bulkUpdateOrder(db, map[uuid.UUID]int{id: 8})
	assert.ErrorIs(t, err, databaseErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBulkUpdateOrderEmptyInputKeepsTransactionalNoOp(t *testing.T) {
	db, mock := newMockMomentRepositoryDB(t)

	mock.ExpectBegin()
	mock.ExpectCommit()

	require.NoError(t, bulkUpdateOrder(db, map[uuid.UUID]int{}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPublicWallOrderClausesPrioritizeManualOrderWithChronologicalFallback(t *testing.T) {
	assert.Equal(t, []string{
		`CASE WHEN "order" > 0 THEN 0 ELSE 1 END ASC`,
		`"order" ASC`,
		"created_at DESC",
		"id DESC",
	}, publicWallOrderClauses())
}

func TestPublicWallCursorWhereClauseMatchesManualOrderSort(t *testing.T) {
	clause := publicWallCursorWhereClause()

	assert.Contains(t, clause, `CASE WHEN "order" > 0 THEN 0 ELSE 1 END`)
	assert.Contains(t, clause, `"order" > ?`)
	assert.Contains(t, clause, "created_at < ?")
	assert.Contains(t, clause, "id < ?::uuid")
	assert.NotContains(t, clause, "id::text")
}

func TestLatestPublicMomentUpdatedAtQueryUsesPublicWallVisibility(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	var latest sql.NullTime
	stmt := latestPublicMomentUpdatedAtQuery(db, eventID).Scan(&latest).Statement
	sqlText := strings.ToLower(stmt.SQL.String())

	assert.Contains(t, sqlText, "from \"moments\"")
	assert.Contains(t, sqlText, "event_id")
	assert.Contains(t, sqlText, "is_approved")
	assert.Contains(t, sqlText, "processing_status in")
	assert.Contains(t, sqlText, "deleted_at")
	assert.Contains(t, sqlText, "is_approved = true")
	assert.Contains(t, sqlText, "processing_status in ('', 'done')")
	assert.Equal(t, []interface{}{eventID}, stmt.Vars)
}

func TestPublicWallVisibilityIsLiteralForGenericPreparedPlans(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	var total int64
	stmt := applyPublicWallVisibleMomentFilters(db.Model(&models.Moment{}), eventID).
		Count(&total).Statement
	sqlText := strings.ToLower(stmt.SQL.String())

	assert.Contains(t, sqlText, "event_id = $1")
	assert.Contains(t, sqlText, "is_approved = true")
	assert.Contains(t, sqlText, "processing_status in ('', 'done')")
	assert.Contains(t, sqlText, "deleted_at")
	assert.Equal(t, []interface{}{eventID}, stmt.Vars,
		"only request-scoped event_id should be bound; wall-state invariants must stay literal")
}

func TestMomentByEventIDAndContentURLQueryScopesIdempotencyToEventAndActiveRecord(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=localhost user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)

	eventID := uuid.Must(uuid.NewV4())
	contentURL := "moments/" + eventID.String() + "/raw/photo.webp"
	var moment models.Moment
	stmt := momentByEventIDAndContentURLQuery(db, eventID, contentURL).First(&moment).Statement
	sqlText := strings.ToLower(stmt.SQL.String())

	assert.Contains(t, sqlText, "event_id = $1 and content_url = $2")
	assert.Contains(t, sqlText, "deleted_at")
	assert.Equal(t, []interface{}{eventID, contentURL, 1}, stmt.Vars)
}

func TestLoadPublicWallPageStartsCountAndItemsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan struct{})

	var (
		items []models.Moment
		total int64
		err   error
	)
	go func() {
		defer close(done)
		items, total, err = loadPublicWallPage(
			func(context.Context) (int64, error) {
				started <- "count"
				<-release
				return 42, nil
			},
			func(context.Context) ([]models.Moment, error) {
				started <- "items"
				<-release
				return []models.Moment{{ID: uuid.Must(uuid.NewV4())}}, nil
			},
		)
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			t.Fatal("count and item queries did not both start before either completed")
		}
	}
	close(release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("parallel wall-page load did not finish")
	}
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"count": true, "items": true}, seen)
	assert.EqualValues(t, 42, total)
	require.Len(t, items, 1)
}

func TestLoadPublicWallPagePreservesCountErrorPriority(t *testing.T) {
	countErr := errors.New("count failed")
	itemsErr := errors.New("items failed")

	items, total, err := loadPublicWallPage(
		func(context.Context) (int64, error) { return 0, countErr },
		func(context.Context) ([]models.Moment, error) { return nil, itemsErr },
	)

	assert.ErrorIs(t, err, countErr)
	assert.Nil(t, items)
	assert.Zero(t, total)
}

func TestLoadPublicWallPageRelaysWorkerPanicToCaller(t *testing.T) {
	assert.PanicsWithValue(t, "count loader panic", func() {
		_, _, _ = loadPublicWallPage(
			func(context.Context) (int64, error) { panic("count loader panic") },
			func(context.Context) ([]models.Moment, error) { return []models.Moment{}, nil },
		)
	})
}

func TestLoadPublicWallPageKeepsCountErrorAheadOfItemsPanic(t *testing.T) {
	countErr := errors.New("count failed")

	assert.NotPanics(t, func() {
		items, total, err := loadPublicWallPage(
			func(context.Context) (int64, error) { return 0, countErr },
			func(context.Context) ([]models.Moment, error) { panic("items loader panic") },
		)

		assert.ErrorIs(t, err, countErr)
		assert.Nil(t, items)
		assert.Zero(t, total)
	})
}

func TestLoadPublicWallPageCancelsAndJoinsItemsAfterCountError(t *testing.T) {
	countErr := errors.New("count failed")
	itemsStarted := make(chan struct{})
	itemsStopped := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, _, err := loadPublicWallPage(
			func(context.Context) (int64, error) {
				<-itemsStarted
				return 0, countErr
			},
			func(ctx context.Context) ([]models.Moment, error) {
				defer close(itemsStopped)
				close(itemsStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			},
		)
		done <- err
	}()

	select {
	case <-itemsStarted:
	case <-time.After(time.Second):
		t.Fatal("items loader did not start")
	}

	select {
	case err := <-done:
		assert.ErrorIs(t, err, countErr)
	case <-time.After(time.Second):
		t.Fatal("count error did not cancel and join the items loader")
	}

	select {
	case <-itemsStopped:
	case <-time.After(time.Second):
		t.Fatal("items loader was not joined before return")
	}
}

func TestLoadPublicWallPageItemsErrorCancelsCountWithoutMaskingOrigin(t *testing.T) {
	itemsErr := errors.New("items failed")
	countStarted := make(chan struct{})

	items, total, err := loadPublicWallPage(
		func(ctx context.Context) (int64, error) {
			close(countStarted)
			<-ctx.Done()
			return 0, ctx.Err()
		},
		func(context.Context) ([]models.Moment, error) {
			<-countStarted
			return nil, itemsErr
		},
	)

	assert.ErrorIs(t, err, itemsErr)
	assert.NotErrorIs(t, err, context.Canceled)
	assert.Nil(t, items)
	assert.Zero(t, total)
}

// BenchmarkBulkUpdateOrderPostgres compares the former N-round-trip
// implementation with the production batch statement against real
// PostgreSQL. It is opt-in so ordinary unit suites stay hermetic:
//
//	EVENTIAPP_BENCHMARK_DSN="host=127.0.0.1 user=postgres password=postgres dbname=events_db port=15432 sslmode=disable" \
//	go test ./repositories/momentrepository -run '^$' -bench BenchmarkBulkUpdateOrderPostgres -benchtime=10x
func BenchmarkBulkUpdateOrderPostgres(b *testing.B) {
	dsn := strings.TrimSpace(os.Getenv("EVENTIAPP_BENCHMARK_DSN"))
	if dsn == "" {
		b.Skip("set EVENTIAPP_BENCHMARK_DSN to run the PostgreSQL round-trip benchmark")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(b, err)
	sqlDB, err := db.DB()
	require.NoError(b, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	b.Cleanup(func() { require.NoError(b, sqlDB.Close()) })

	require.NoError(b, db.Exec(`
		CREATE TEMP TABLE moments (
			id uuid PRIMARY KEY,
			"order" integer,
			created_at timestamptz,
			updated_at timestamptz,
			deleted_at timestamptz
		) ON COMMIT PRESERVE ROWS
	`).Error)
	b.Cleanup(func() { _ = db.Exec(`DROP TABLE IF EXISTS pg_temp.moments`).Error })

	const updateCount = 500
	updates := make(map[uuid.UUID]int, updateCount)
	for i := 0; i < updateCount; i++ {
		id := uuid.Must(uuid.NewV4())
		updates[id] = updateCount - i
	}
	payload, err := encodeMomentOrderUpdates(updates)
	require.NoError(b, err)
	require.NoError(b, db.Exec(`
		INSERT INTO moments (id, "order", created_at, updated_at)
		SELECT requested.id, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		FROM jsonb_to_recordset(?::jsonb) AS requested(id uuid, new_order integer)
	`, string(payload)).Error)

	b.Run("legacy_500_round_trips", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			require.NoError(b, legacyBulkUpdateOrder(db, updates))
		}
	})
	b.Run("batch_one_round_trip", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			require.NoError(b, bulkUpdateOrder(db, updates))
		}
	})
}

func legacyBulkUpdateOrder(db *gorm.DB, updates map[uuid.UUID]int) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for id, order := range updates {
			if err := tx.Model(&models.Moment{}).
				Where("id = ?", id).
				Update("\"order\"", order).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
