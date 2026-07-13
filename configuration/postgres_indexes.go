package configuration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	publicMomentsWallIndexName = "idx_moments_public_wall_v1"

	// The session-level lock serializes this migration across concurrently
	// starting API replicas. 0x45564e544d4f4d31 spells "EVNTMOM1" and is
	// intentionally stable across releases.
	publicMomentsWallIndexAdvisoryKey int64 = 0x45564e544d4f4d31

	performanceIndexMigrationTimeout = 30 * time.Minute
	performanceIndexCleanupTimeout   = 5 * time.Second
)

// publicMomentsWallIndexDDL matches the public wall's visibility predicate
// and complete ordering. The partial predicate keeps failed, processing,
// rejected, and soft-deleted moments out of the index.
//
// CONCURRENTLY is deliberate: this migration runs outside a transaction and
// avoids blocking inserts/updates/deletes while an existing table is indexed.
const publicMomentsWallIndexDDL = `
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_moments_public_wall_v1
ON moments (
    event_id,
    (CASE WHEN "order" > 0 THEN 0 ELSE 1 END) ASC,
    "order" ASC,
    created_at DESC,
    id DESC
)
WHERE deleted_at IS NULL
  AND is_approved = true
  AND processing_status IN ('', 'done')
`

const publicMomentsWallIndexDropDDL = `
DROP INDEX CONCURRENTLY IF EXISTS idx_moments_public_wall_v1
`

type postgresIndexState struct {
	exists        bool
	valid         bool
	ready         bool
	matches       bool
	qualifiedName string
}

type performanceIndexEnsurer func(*gorm.DB) error

// MigratePerformanceIndexes runs optional access-path migrations. Run this in
// the background after critical schema migration: a missing performance index
// can make a query slower, but must never keep the API from becoming ready.
// Every process start schedules another attempt, and the advisory lock makes
// overlapping replica attempts safe.
func MigratePerformanceIndexes() {
	migratePerformanceIndexes(DB, EnsurePerformanceIndexes, slog.Default())
}

func migratePerformanceIndexes(db *gorm.DB, ensure performanceIndexEnsurer, logger *slog.Logger) {
	defer func() {
		if panicValue := recover(); panicValue != nil {
			logger.Warn(
				"optional performance index migration panicked; API remains available",
				"panic", panicValue,
				"retry", "next startup",
			)
		}
	}()

	if err := ensure(db); err != nil {
		logger.Warn(
			"optional performance index migration failed; API remains available",
			"error", err,
			"retry", "next startup",
		)
		return
	}
	logger.Info("performance indexes ready")
}

// EnsurePerformanceIndexes installs hand-tuned PostgreSQL indexes that cannot
// be expressed safely through AutoMigrate. It is safe to call from multiple
// replicas: a session advisory lock serializes inspection and construction,
// and the index itself is built concurrently outside a transaction.
func EnsurePerformanceIndexes(db *gorm.DB) error {
	if db == nil {
		return errors.New("ensure performance indexes: nil database")
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("ensure performance indexes: get sql database: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), performanceIndexMigrationTimeout)
	defer cancel()

	if err := ensurePublicMomentsWallIndex(ctx, sqlDB); err != nil {
		return fmt.Errorf("ensure performance indexes: public moments wall: %w", err)
	}
	return nil
}

func ensurePublicMomentsWallIndex(ctx context.Context, db *sql.DB) (returnErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve migration connection: %w", err)
	}
	defer conn.Close()

	lockConfirmed := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), performanceIndexCleanupTimeout)
		defer cancel()

		// Install cleanup before attempting the lock: a context cancellation can
		// race with the server acquiring it even when ExecContext reports an
		// error. Unlock first, then reset every session setting before returning
		// this connection to the request pool.
		var cleanupErrors []error
		var unlocked bool
		if err := conn.QueryRowContext(
			cleanupCtx,
			`SELECT pg_advisory_unlock($1)`,
			publicMomentsWallIndexAdvisoryKey,
		).Scan(&unlocked); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("release migration advisory lock: %w", err))
		} else if lockConfirmed && !unlocked {
			cleanupErrors = append(cleanupErrors, errors.New("release migration advisory lock: session did not hold lock"))
		}
		if _, err := conn.ExecContext(cleanupCtx, `RESET lock_timeout`); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("reset migration lock timeout: %w", err))
		}
		if _, err := conn.ExecContext(cleanupCtx, `RESET statement_timeout`); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("reset migration statement timeout: %w", err))
		}

		if len(cleanupErrors) > 0 {
			// sql.Conn.Close returns the physical session to the pool. Marking it
			// bad is required so advisory locks or SET values cannot leak into an
			// unrelated API request when cleanup was incomplete.
			if err := discardSQLConnection(conn); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("discard migration connection: %w", err))
			}
			returnErr = errors.Join(append([]error{returnErr}, cleanupErrors...)...)
		}
	}()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, publicMomentsWallIndexAdvisoryKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	lockConfirmed = true

	if _, err := conn.ExecContext(ctx, `SET lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("set migration lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `SET statement_timeout = '25min'`); err != nil {
		return fmt.Errorf("set migration statement timeout: %w", err)
	}

	state, err := readPostgresIndexState(ctx, conn, publicMomentsWallIndexName)
	if err != nil {
		return err
	}
	if state.exists && state.valid && state.ready && state.matches {
		return nil
	}

	// PostgreSQL can leave an invalid catalog entry when a concurrent build is
	// interrupted, and a manual migration can leave a valid homonym with the
	// wrong keys or predicate. IF NOT EXISTS would silently keep either entry,
	// so remove it before retrying the versioned build.
	if state.exists {
		dropDDL := `DROP INDEX CONCURRENTLY IF EXISTS ` + state.qualifiedName
		if _, err := conn.ExecContext(ctx, dropDDL); err != nil {
			return fmt.Errorf("drop invalid index %s: %w", publicMomentsWallIndexName, err)
		}
	}

	if _, err := conn.ExecContext(ctx, publicMomentsWallIndexDDL); err != nil {
		return fmt.Errorf("create index %s concurrently: %w", publicMomentsWallIndexName, err)
	}

	state, err = readPostgresIndexState(ctx, conn, publicMomentsWallIndexName)
	if err != nil {
		return err
	}
	if !state.exists || !state.valid || !state.ready || !state.matches {
		return fmt.Errorf(
			"index %s did not become ready (exists=%t valid=%t ready=%t matches=%t)",
			publicMomentsWallIndexName,
			state.exists,
			state.valid,
			state.ready,
			state.matches,
		)
	}
	return nil
}

func readPostgresIndexState(ctx context.Context, conn *sql.Conn, indexName string) (postgresIndexState, error) {
	var state postgresIndexState
	var (
		onTargetTable  bool
		accessMethod   string
		keyCount       int
		attributeCount int
		key1           string
		key2           string
		key3           string
		key4           string
		key5           string
		keyOptions     string
		predicate      string
	)
	err := conn.QueryRowContext(ctx, `
		SELECT
			indexes.indisvalid,
			indexes.indisready,
			indexes.indrelid = target_table.oid,
			access_method.amname,
			indexes.indnkeyatts,
			indexes.indnatts,
			COALESCE(pg_get_indexdef(indexes.indexrelid, 1, true), ''),
			COALESCE(pg_get_indexdef(indexes.indexrelid, 2, true), ''),
			COALESCE(pg_get_indexdef(indexes.indexrelid, 3, true), ''),
			COALESCE(pg_get_indexdef(indexes.indexrelid, 4, true), ''),
			COALESCE(pg_get_indexdef(indexes.indexrelid, 5, true), ''),
			indexes.indoption::text,
			COALESCE(pg_get_expr(indexes.indpred, indexes.indrelid, true), ''),
			quote_ident(namespace.nspname) || '.' || quote_ident(index_class.relname)
		FROM pg_catalog.pg_class AS target_table
		JOIN pg_catalog.pg_namespace AS namespace
		  ON namespace.oid = target_table.relnamespace
		JOIN pg_catalog.pg_class AS index_class
		  ON index_class.relnamespace = target_table.relnamespace
		 AND index_class.relname = $1
		JOIN pg_catalog.pg_index AS indexes
		  ON indexes.indexrelid = index_class.oid
		JOIN pg_catalog.pg_am AS access_method
		  ON access_method.oid = index_class.relam
		WHERE target_table.oid = to_regclass('moments')
	`, indexName).Scan(
		&state.valid,
		&state.ready,
		&onTargetTable,
		&accessMethod,
		&keyCount,
		&attributeCount,
		&key1,
		&key2,
		&key3,
		&key4,
		&key5,
		&keyOptions,
		&predicate,
		&state.qualifiedName,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return postgresIndexState{}, nil
	}
	if err != nil {
		return postgresIndexState{}, fmt.Errorf("inspect index %s: %w", indexName, err)
	}
	state.exists = true
	state.matches = onTargetTable &&
		accessMethod == "btree" &&
		keyCount == 5 &&
		attributeCount == 5 &&
		normalizeIndexComponent(key1) == "event_id" &&
		normalizeIndexComponent(key2) == `casewhen"order">0then0else1end` &&
		normalizeIndexComponent(key3) == `"order"` &&
		normalizeIndexComponent(key4) == "created_at" &&
		normalizeIndexComponent(key5) == "id" &&
		strings.Join(strings.Fields(keyOptions), " ") == "0 0 0 3 3" &&
		normalizeIndexPredicate(predicate) == `deleted_atisnullandis_approved=trueandprocessing_status=anyarray['','done']`
	return state, nil
}

func normalizeIndexComponent(value string) string {
	value = strings.ToLower(value)
	return strings.NewReplacer(
		" ", "",
		"\t", "",
		"\r", "",
		"\n", "",
		"(", "",
		")", "",
	).Replace(value)
}

func normalizeIndexPredicate(value string) string {
	value = normalizeIndexComponent(value)
	return strings.NewReplacer(
		"::charactervarying[]", "",
		"::charactervarying", "",
		"::varchar[]", "",
		"::varchar", "",
		"::text[]", "",
		"::text", "",
	).Replace(value)
}

func discardSQLConnection(conn *sql.Conn) error {
	if conn == nil {
		return nil
	}
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	return err
}
