package configuration

import (
	"fmt"

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
// Each distinct value is tested by PostgreSQL itself, so only zones it rejects
// are touched (POSIX-style zones it accepts are left alone). A blank timezone
// reads as UTC in SQL and as the default zone in the dashboard. Idempotent: a
// second run finds nothing to repair. It does not change updated_at.
const repairInvalidEventTimezonesStatement = `DO $$
DECLARE
  zone text;
BEGIN
  FOR zone IN SELECT DISTINCT timezone FROM events WHERE timezone IS NOT NULL AND btrim(timezone) <> '' LOOP
    BEGIN
      PERFORM now() AT TIME ZONE zone;
    EXCEPTION WHEN invalid_parameter_value THEN
      UPDATE events SET timezone = '' WHERE timezone = zone;
    END;
  END LOOP;
END $$`

func repairInvalidEventTimezones(tx *gorm.DB) error {
	if err := tx.Exec(repairInvalidEventTimezonesStatement).Error; err != nil {
		return fmt.Errorf("repair invalid event timezones: %w", err)
	}
	return nil
}
