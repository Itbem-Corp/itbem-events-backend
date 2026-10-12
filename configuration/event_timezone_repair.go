package configuration

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/gofrs/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// repairInvalidEventTimezones blanks events.timezone values PostgreSQL does
// not recognize (EVT-3, SET-16). The list, count, overview and notification
// queries compute `event_date_time AT TIME ZONE COALESCE(NULLIF(timezone,
// ''), 'UTC')`; one unknown zone made every query that touched the row fail,
// taking down the whole organization's events list (and the root portfolio).
// The API validates new values (dtos.ValidateEventTimezone); this cleans
// values stored before that validation existed.
//
// Each distinct value is tested by PostgreSQL itself (inside a savepoint, so
// a rejected zone does not abort the migration transaction), and only zones
// it rejects are touched (POSIX-style zones it accepts are left alone). A
// blank timezone reads as UTC in SQL and as the default zone in the
// dashboard. Every repair is logged at Info with the zone values, the count
// and the event ids (no personal data), so the data change is auditable.
// Idempotent: a second run finds nothing to repair. It does not change
// updated_at.
func repairInvalidEventTimezones(tx *gorm.DB) error {
	var zones []string
	if err := tx.Raw(`SELECT DISTINCT timezone FROM events WHERE timezone IS NOT NULL AND btrim(timezone) <> ''`).
		Scan(&zones).Error; err != nil {
		return fmt.Errorf("repair invalid event timezones: list zones: %w", err)
	}
	var invalidZones []string
	var repairedIDs []uuid.UUID
	for i, zone := range zones {
		savepoint := fmt.Sprintf("event_timezone_check_%d", i)
		if err := tx.Exec("SAVEPOINT " + savepoint).Error; err != nil {
			return fmt.Errorf("repair invalid event timezones: savepoint: %w", err)
		}
		checkErr := tx.Exec(`SELECT now() AT TIME ZONE ?`, zone).Error
		if checkErr == nil {
			if err := tx.Exec("RELEASE SAVEPOINT " + savepoint).Error; err != nil {
				return fmt.Errorf("repair invalid event timezones: release savepoint: %w", err)
			}
			continue
		}
		if err := tx.Exec("ROLLBACK TO SAVEPOINT " + savepoint).Error; err != nil {
			return fmt.Errorf("repair invalid event timezones: rollback savepoint: %w", err)
		}
		if !isInvalidTimezoneError(checkErr) {
			return fmt.Errorf("repair invalid event timezones: check zone: %w", checkErr)
		}
		var ids []uuid.UUID
		if err := tx.Raw(`UPDATE events SET timezone = '' WHERE timezone = ? RETURNING id`, zone).
			Scan(&ids).Error; err != nil {
			return fmt.Errorf("repair invalid event timezones: blank zone: %w", err)
		}
		invalidZones = append(invalidZones, zone)
		repairedIDs = append(repairedIDs, ids...)
	}
	if len(repairedIDs) > 0 {
		slog.Info("event timezone repair: blanked timezones PostgreSQL rejects",
			"count", len(repairedIDs), "zones", invalidZones, "event_ids", repairedIDs)
	}
	return nil
}

// isInvalidTimezoneError reports PostgreSQL's invalid_parameter_value (22023),
// the error AT TIME ZONE raises for an unknown zone.
func isInvalidTimezoneError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22023"
}
