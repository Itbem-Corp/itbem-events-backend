package dtos

import (
	"errors"
	"strings"
	"time"

	// Embed the IANA database so validation does not depend on the image's
	// /usr/share/zoneinfo (the runtime image is debian-slim).
	_ "time/tzdata"
)

// ErrInvalidEventTimezone is returned (wrapped in a field error) when an event
// payload carries a timezone that is not an IANA zone name. An invalid value
// used to be stored and then broke every list, count and overview query that
// does `event_date_time AT TIME ZONE events.timezone` (EVT-3, SET-16).
var ErrInvalidEventTimezone = errors.New("zona horaria inválida")

// ValidateEventTimezone accepts "" (the default zone is applied later) and any
// IANA zone name time.LoadLocation knows, such as "America/Mexico_City" or
// "UTC". "Local" is rejected: it names the server's zone, not the event's,
// and PostgreSQL does not know it.
func ValidateEventTimezone(timezone string) error {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return nil
	}
	if strings.EqualFold(timezone, "local") {
		return ErrInvalidEventTimezone
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return ErrInvalidEventTimezone
	}
	return nil
}
