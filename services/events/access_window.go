package events

import (
	"time"

	"events-stocks/models"
)

// Access window states exposed to the public site in meta.access.windowStatus.
const (
	AccessWindowOpen       = "open"
	AccessWindowNotStarted = "not_started"
	AccessWindowEnded      = "ended"
)

// EventAccessWindowStatus reports where now falls relative to the event access
// window ("Disponible desde/hasta"). A zero time or a year <= 1970 counts as
// unset. This is the single rule used by every public read gate.
func EventAccessWindowStatus(cfg *models.EventConfig, now time.Time) string {
	if cfg == nil {
		return AccessWindowOpen
	}
	if EventAccessTimeSet(cfg.ActiveFrom) && now.Before(cfg.ActiveFrom) {
		return AccessWindowNotStarted
	}
	if cfg.ActiveUntil != nil && EventAccessTimeSet(*cfg.ActiveUntil) && now.After(*cfg.ActiveUntil) {
		return AccessWindowEnded
	}
	return AccessWindowOpen
}

// EventAccessWindowOpen reports whether now is inside the event access window.
func EventAccessWindowOpen(cfg *models.EventConfig, now time.Time) bool {
	return EventAccessWindowStatus(cfg, now) == AccessWindowOpen
}

// EventAccessTimeSet treats zero and pre-1971 timestamps as "not configured".
func EventAccessTimeSet(value time.Time) bool {
	return !value.IsZero() && value.Year() > 1970
}
