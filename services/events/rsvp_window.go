package events

import (
	"time"

	"events-stocks/models"
)

// RSVPOpen reports whether guests may submit an RSVP from the public site: the
// RSVP block is shown (show_rsvp_section, with legacy visibility defaults) and
// the rsvp_deadline, when set, has not passed. Host edits from the dashboard do
// not go through this rule.
func RSVPOpen(cfg *models.EventConfig, now time.Time) bool {
	cfg = effectivePageSpecConfig(cfg)
	if cfg == nil {
		return true
	}
	return cfg.ShowRSVPSection && !cfg.RSVPDeadlinePassed(now)
}

// RSVPDeadline returns the configured deadline, or nil when none is set.
func RSVPDeadline(cfg *models.EventConfig) *time.Time {
	if cfg == nil || cfg.RSVPDeadline == nil || !EventAccessTimeSet(*cfg.RSVPDeadline) {
		return nil
	}
	deadline := cfg.RSVPDeadline.UTC()
	return &deadline
}
