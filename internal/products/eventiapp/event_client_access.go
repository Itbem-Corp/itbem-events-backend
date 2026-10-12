package eventiapp

import (
	"log/slog"
	"strings"
	"sync/atomic"
)

// eventClientAccess holds the parsed EVENT_CLIENT_ACCESS flag. Off by default.
var eventClientAccess atomic.Bool

// ParseEventClientAccess maps the EVENT_CLIENT_ACCESS value to on/off. Only
// "on" enables the feature; empty is off, and any other value is logged and
// treated as off so a typo can never open access.
func ParseEventClientAccess(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on":
		return true
	case "", "off":
		return false
	default:
		slog.Error("invalid EVENT_CLIENT_ACCESS value; event client access stays off", "allowed", "on|off")
		return false
	}
}

// SetEventClientAccess records the flag once at startup (and in tests).
func SetEventClientAccess(enabled bool) { eventClientAccess.Store(enabled) }

// EventClientAccessEnabled reports whether event clients are enabled.
func EventClientAccessEnabled() bool { return eventClientAccess.Load() }
