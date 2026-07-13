//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"events-stocks/configuration"
	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestPerformanceIndexMigrationIsConcurrentAndIdempotent(t *testing.T) {
	const indexName = "idx_moments_public_wall_v1"
	db := configuration.DB
	require.NoError(t, db.Exec(`DROP INDEX CONCURRENTLY IF EXISTS idx_moments_public_wall_v1`).Error)

	// Simulate two API replicas starting against the same schema. The
	// session-level advisory lock must serialize them without a duplicate
	// object error or an invalid concurrent index.
	var wg sync.WaitGroup
	errorsByReplica := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsByReplica <- configuration.EnsurePerformanceIndexes(db)
		}()
	}
	wg.Wait()
	close(errorsByReplica)
	for err := range errorsByReplica {
		require.NoError(t, err)
	}

	var valid, ready bool
	var definition string
	require.NoError(t, db.Raw(`
		SELECT indexes.indisvalid, indexes.indisready, pg_get_indexdef(indexes.indexrelid)
		FROM pg_catalog.pg_class AS index_class
		JOIN pg_catalog.pg_index AS indexes ON indexes.indexrelid = index_class.oid
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = index_class.relnamespace
		WHERE namespace.nspname = current_schema()
		  AND index_class.relname = ?
	`, indexName).Row().Scan(&valid, &ready, &definition))
	assert.True(t, valid)
	assert.True(t, ready)

	normalizedDefinition := strings.ToLower(strings.Join(strings.Fields(definition), " "))
	assert.Contains(t, normalizedDefinition, "case when (\"order\" > 0)")
	assert.Contains(t, normalizedDefinition, "created_at desc")
	assert.Contains(t, normalizedDefinition, "id desc")
	assert.Contains(t, normalizedDefinition, "deleted_at is null")
	assert.Contains(t, normalizedDefinition, "is_approved = true")
	assert.Contains(t, normalizedDefinition, "processing_status")

	// A later deployment should only inspect the valid index and return.
	require.NoError(t, configuration.EnsurePerformanceIndexes(db))

	var advisoryLocks int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM pg_catalog.pg_locks WHERE locktype = 'advisory'`).Scan(&advisoryLocks).Error)
	assert.Zero(t, advisoryLocks, "migration session advisory lock must be released")
}

func TestPerformanceIndexMigrationRepairsWrongHomonymousDefinition(t *testing.T) {
	db := configuration.DB
	require.NoError(t, db.Exec(`DROP INDEX CONCURRENTLY IF EXISTS idx_moments_public_wall_v1`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX CONCURRENTLY idx_moments_public_wall_v1 ON moments (event_id)`).Error)

	require.NoError(t, configuration.EnsurePerformanceIndexes(db))

	var definition string
	require.NoError(t, db.Raw(`
		SELECT pg_get_indexdef(indexes.indexrelid)
		FROM pg_catalog.pg_class AS index_class
		JOIN pg_catalog.pg_index AS indexes ON indexes.indexrelid = index_class.oid
		WHERE indexes.indrelid = to_regclass('moments')
		  AND index_class.relname = 'idx_moments_public_wall_v1'
	`).Scan(&definition).Error)
	normalized := strings.ToLower(strings.Join(strings.Fields(definition), " "))
	assert.Contains(t, normalized, `case when ("order" > 0)`)
	assert.Contains(t, normalized, `"order", created_at desc, id desc`)
	assert.Contains(t, normalized, `processing_status`)
}

func TestPerformanceIndexMigrationUsesResolvedMomentsSchema(t *testing.T) {
	suffix := strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:8]
	firstSchema := "wall_shadow_" + suffix
	targetSchema := "wall_target_" + suffix

	db, err := gorm.Open(gormpostgres.Open(testPostgresConnectionString), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = db.Exec(`SET search_path = public`).Error
		_ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, firstSchema)).Error
		_ = db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, targetSchema)).Error
		_ = sqlDB.Close()
	})

	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, firstSchema)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, targetSchema)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`
		CREATE TABLE %s.moments (
			id uuid PRIMARY KEY,
			event_id uuid,
			"order" bigint,
			created_at timestamptz,
			deleted_at timestamptz,
			is_approved boolean,
			processing_status varchar(20)
		)
	`, targetSchema)).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(`SET search_path = %s, %s`, firstSchema, targetSchema)).Error)

	var currentSchema string
	require.NoError(t, db.Raw(`SELECT current_schema()`).Scan(&currentSchema).Error)
	require.Equal(t, firstSchema, currentSchema)

	require.NoError(t, configuration.EnsurePerformanceIndexes(db))

	var indexedTable string
	require.NoError(t, db.Raw(`
		SELECT table_namespace.nspname || '.' || table_class.relname
		FROM pg_catalog.pg_class AS index_class
		JOIN pg_catalog.pg_index AS indexes ON indexes.indexrelid = index_class.oid
		JOIN pg_catalog.pg_class AS table_class ON table_class.oid = indexes.indrelid
		JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_class.relnamespace
		WHERE index_class.relname = 'idx_moments_public_wall_v1'
		  AND table_namespace.nspname = ?
	`, targetSchema).Scan(&indexedTable).Error)
	assert.Equal(t, targetSchema+".moments", indexedTable)
}
