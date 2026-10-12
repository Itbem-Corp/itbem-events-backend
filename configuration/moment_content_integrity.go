package configuration

import (
	"fmt"
	"log/slog"

	"events-stocks/models"

	"gorm.io/gorm"
)

// momentLiveContentURLIndexDDL makes one stored object back at most one live
// moment per event, so two concurrent confirms of the same upload cannot both
// insert a moment (MOM-6). Text-only moments (empty content_url) and
// soft-deleted rows are outside the predicate.
var momentLiveContentURLIndexDDL = `CREATE UNIQUE INDEX IF NOT EXISTS ` + models.MomentLiveContentURLIndex + `
ON moments (event_id, content_url)
WHERE deleted_at IS NULL AND content_url <> ''`

// momentLiveContentURLDedupeSQL soft-deletes the extra live copies that the
// confirm race may already have created, keeping per (event_id, content_url)
// the most advanced row: approved first, then processed, then the oldest. It
// only sets deleted_at, so a wrongly hidden row can be restored by clearing it;
// the storage objects are shared by the copies and are not touched.
const momentLiveContentURLDedupeSQL = `
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY event_id, content_url
               ORDER BY is_approved DESC,
                        CASE WHEN processing_status IN ('', 'done') THEN 0 ELSE 1 END,
                        created_at ASC,
                        id ASC
           ) AS copy_rank
    FROM moments
    WHERE deleted_at IS NULL AND content_url <> '' AND event_id IS NOT NULL
)
UPDATE moments
SET deleted_at = NOW()
FROM ranked
WHERE moments.id = ranked.id AND ranked.copy_rank > 1`

// migrateMomentContentURLUniqueness installs the live content_url unique
// index once. It runs inside the startup migration transaction (after
// AutoMigrate created moments): when the index already exists it returns
// without locking; otherwise it locks moments against writes for the dedupe
// and the index build, so no new duplicate can slip in between.
func migrateMomentContentURLUniqueness(tx *gorm.DB) error {
	var exists bool
	if err := tx.Raw(`SELECT EXISTS (
		SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?
	)`, models.MomentLiveContentURLIndex).Scan(&exists).Error; err != nil {
		return fmt.Errorf("inspect moment content index: %w", err)
	}
	if exists {
		return nil
	}
	if err := tx.Exec(`LOCK TABLE moments IN SHARE ROW EXCLUSIVE MODE`).Error; err != nil {
		return fmt.Errorf("lock moments for content index: %w", err)
	}
	result := tx.Exec(momentLiveContentURLDedupeSQL)
	if result.Error != nil {
		return fmt.Errorf("soft-delete duplicate moments: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		slog.Warn("soft-deleted duplicate live moments before adding the content_url unique index",
			"rows", result.RowsAffected, "index", models.MomentLiveContentURLIndex)
	}
	if err := tx.Exec(momentLiveContentURLIndexDDL).Error; err != nil {
		return fmt.Errorf("create moment content index: %w", err)
	}
	return nil
}
