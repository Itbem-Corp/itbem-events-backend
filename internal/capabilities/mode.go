package capabilities

import (
	"fmt"
	"strings"
)

// Mode is the rollout state of a server-side authorization check.
//   - off: the check is not evaluated (the behaviour before the check existed).
//   - shadow: the check is evaluated and a would-deny is logged, but the
//     decision does not change.
//   - enforce: a failed check denies the request.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeShadow  Mode = "shadow"
	ModeEnforce Mode = "enforce"
)

// ParseMode accepts off|shadow|enforce (case-insensitive). Empty means off.
// Any other value returns off and an error so the caller can log it: an
// invalid flag must never turn enforcement on by accident.
func ParseMode(raw string) (Mode, error) {
	switch value := strings.ToLower(strings.TrimSpace(raw)); value {
	case "", string(ModeOff):
		return ModeOff, nil
	case string(ModeShadow):
		return ModeShadow, nil
	case string(ModeEnforce):
		return ModeEnforce, nil
	default:
		return ModeOff, fmt.Errorf("invalid authorization mode %q: expected off, shadow or enforce", raw)
	}
}
