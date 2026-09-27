//go:build integration

package integration_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errRollbackHeartbeatProtocolMigrationFixture = errors.New("rollback disposable heartbeat migration fixture")

// TestAutomationAgentHeartbeatProtocolMigrationUpgradesLegacyRows verifies
// that AutoMigrate adds the new protocol column to the pre-protocol heartbeat
// schema and classifies existing worker rows as legacy ([]), not as capable.
// The fixture is a uniquely named disposable table and the enclosing database
// transaction is deliberately rolled back on success as well as on failure.
func TestAutomationAgentHeartbeatProtocolMigrationUpgradesLegacyRows(t *testing.T) {
	db := configuration.DB
	require.NotNil(t, db, "integration TestMain must provide disposable PostgreSQL")

	tableName := "itbem_hb_proto_" + strings.ReplaceAll(uuid.Must(uuid.NewV4()).String(), "-", "")[:12]
	quotedTable := `"` + tableName + `"`
	legacyID := uuid.Must(uuid.NewV4())
	workerID := uuid.Must(uuid.NewV4()).String()
	now := time.Now().UTC().Truncate(time.Microsecond)

	err := db.Transaction(func(tx *gorm.DB) error {
		// Match the heartbeat model embedded in the active runtime build
		// (revision 32ae78e739c6b062304bf5a866d6d2a662a31202), before protocol
		// negotiation was introduced. AutoMigrate may add newer columns, but
		// this fixture starts from the known deployed shape. The random table
		// name ensures no shared application table or another test's fixture is
		// altered. The deployed model used a GORM uniqueIndex, so model that as
		// the canonical standalone index rather than an inline SQL UNIQUE
		// constraint (which PostgreSQL names differently and GORM interprets as
		// a column-level unique constraint).
		createLegacyTable := fmt.Sprintf(`CREATE TABLE %s (
			id uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
			worker_id varchar(64) NOT NULL,
			provider varchar(48) NOT NULL DEFAULT '',
			model varchar(128) NOT NULL DEFAULT '',
			role varchar(32) NOT NULL DEFAULT '',
			lane varchar(32) NOT NULL DEFAULT '',
			concurrency integer NOT NULL DEFAULT 1,
			workspace_readiness jsonb NOT NULL DEFAULT '[]',
			started_at timestamptz NOT NULL,
			last_seen_at timestamptz NOT NULL,
			created_at timestamptz NOT NULL,
			updated_at timestamptz NOT NULL
		)`, quotedTable)
		if err := tx.Exec(createLegacyTable).Error; err != nil {
			return fmt.Errorf("create disposable legacy heartbeat table: %w", err)
		}
		legacyWorkerIndex := tx.NamingStrategy.IndexName(tableName, "worker_id")
		if err := tx.Exec(fmt.Sprintf(`CREATE UNIQUE INDEX "%s" ON %s (worker_id)`, legacyWorkerIndex, quotedTable)).Error; err != nil {
			return fmt.Errorf("create legacy heartbeat worker index: %w", err)
		}
		if err := tx.Exec(fmt.Sprintf(`INSERT INTO %s
			(id, worker_id, provider, model, role, lane, concurrency, workspace_readiness,
			 started_at, last_seen_at, created_at, updated_at)
			VALUES (?, ?, 'openai', 'migration-fixture', 'engineering', 'delivery', 2,
			        '[{"ready":true}]'::jsonb, ?, ?, ?, ?)`, quotedTable),
			legacyID, workerID, now, now, now, now).Error; err != nil {
			return fmt.Errorf("insert legacy heartbeat row: %w", err)
		}

		var legacyColumnCount int64
		if err := tx.Raw(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'protocols_json'`, tableName).
			Scan(&legacyColumnCount).Error; err != nil {
			return fmt.Errorf("inspect legacy heartbeat schema: %w", err)
		}
		if legacyColumnCount != 0 {
			return fmt.Errorf("legacy fixture unexpectedly already contains protocols_json")
		}

		if err := tx.Table(tableName).AutoMigrate(&models.AutomationAgentHeartbeat{}); err != nil {
			return fmt.Errorf("auto migrate disposable legacy heartbeat table: %w", err)
		}

		var migrated struct {
			ID                 uuid.UUID
			WorkerID           string
			Role               string
			Lane               string
			Concurrency        int
			WorkspaceReadiness string `gorm:"column:workspace_readiness"`
			ProtocolsJSON      string `gorm:"column:protocols_json"`
		}
		if err := tx.Raw(fmt.Sprintf(`SELECT id, worker_id, role, lane, concurrency,
			workspace_readiness::text AS workspace_readiness, protocols_json::text AS protocols_json
			FROM %s WHERE id = ?`, quotedTable), legacyID).Scan(&migrated).Error; err != nil {
			return fmt.Errorf("read migrated legacy heartbeat row: %w", err)
		}
		if migrated.ID != legacyID || migrated.WorkerID != workerID {
			return fmt.Errorf("legacy heartbeat identity changed during migration")
		}
		if migrated.Role != "engineering" || migrated.Lane != "delivery" || migrated.Concurrency != 2 || migrated.WorkspaceReadiness != `[{"ready": true}]` {
			return fmt.Errorf("legacy heartbeat metadata changed during migration: role=%q lane=%q concurrency=%d workspace=%q",
				migrated.Role, migrated.Lane, migrated.Concurrency, migrated.WorkspaceReadiness)
		}
		if migrated.ProtocolsJSON != `[]` {
			return fmt.Errorf("legacy heartbeat protocols_json = %q, want []", migrated.ProtocolsJSON)
		}

		var protocolColumn struct {
			IsNullable    string  `gorm:"column:is_nullable"`
			ColumnDefault *string `gorm:"column:column_default"`
			DataType      string  `gorm:"column:data_type"`
		}
		if err := tx.Raw(`SELECT is_nullable, column_default, data_type
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'protocols_json'`, tableName).
			Scan(&protocolColumn).Error; err != nil {
			return fmt.Errorf("inspect migrated protocol column constraints: %w", err)
		}
		if protocolColumn.DataType != "jsonb" || protocolColumn.IsNullable != "NO" || protocolColumn.ColumnDefault == nil || !strings.Contains(*protocolColumn.ColumnDefault, "[]") {
			return fmt.Errorf("protocols_json column constraints = type %q nullable %q default %v, want jsonb NOT NULL DEFAULT []",
				protocolColumn.DataType, protocolColumn.IsNullable, protocolColumn.ColumnDefault)
		}

		// Roll back the isolated DDL and row instead of leaving a test artifact.
		return errRollbackHeartbeatProtocolMigrationFixture
	})
	require.ErrorIs(t, err, errRollbackHeartbeatProtocolMigrationFixture,
		"the isolated schema migration assertions should pass before rollback")
}
