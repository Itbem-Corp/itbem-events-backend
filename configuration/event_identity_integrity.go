package configuration

import (
	"fmt"

	"gorm.io/gorm"
)

// Event name and identifier uniqueness (EVT-2, EVT-6, SET-15).
//
// The first schema put plain UNIQUE indexes on events.name and
// events.identifier (GORM `uniqueIndex` tags). Both counted soft-deleted rows,
// and the name index was global, so one organization's event name blocked
// every other tenant and a deleted event kept its name and slug forever.
//
// The rules now are:
//   - a name is unique per organization among live (not deleted) events;
//   - an identifier (public slug) is unique platform-wide among live events,
//     because public URLs resolve it without an organization.
//
// Existing rows always satisfy the new indexes (each is a subset of an old
// global UNIQUE), so creating them cannot fail on legacy data. Every statement
// is idempotent and runs inside the startup migration transaction, after
// AutoMigrate (the model no longer declares the old indexes).
//
// Rollback note: a binary that still has the `uniqueIndex` tags recreates the
// global indexes on boot; once two live or deleted events share a name or
// slug, that CREATE fails and the old binary cannot start. Deduplicate first.
const (
	EventClientNameLiveIndex = "idx_events_client_name_live"
	EventIdentifierLiveIndex = "idx_events_identifier_live"
)

func eventIdentityIntegrityStatements() []string {
	return []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + EventClientNameLiveIndex + ` ON events (client_id, name) WHERE deleted_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + EventIdentifierLiveIndex + ` ON events (identifier) WHERE deleted_at IS NULL`,
		`DROP INDEX IF EXISTS idx_events_name`,
		`DROP INDEX IF EXISTS idx_events_identifier`,
	}
}

func migrateEventIdentityIntegrity(tx *gorm.DB) error {
	for _, statement := range eventIdentityIntegrityStatements() {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("event identity integrity %q: %w", statement, err)
		}
	}
	return nil
}
