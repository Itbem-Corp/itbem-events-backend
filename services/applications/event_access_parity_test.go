package applications_test

import (
	"sort"
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/services/applications"

	"github.com/stretchr/testify/assert"
)

// The session projection of event-client powers must equal the authz
// ceiling. This lives in an external test package because authz imports
// productmetricshttp, which imports services/applications.
func TestEventClientSessionCapabilitiesMatchAuthzCeiling(t *testing.T) {
	sessionName := map[authz.Capability]string{
		authz.CapabilityView:          "events:view",
		authz.CapabilityEventManage:   "events:manage",
		authz.CapabilityGuestManage:   "guests:manage",
		authz.CapabilityCheckin:       "checkin:run",
		authz.CapabilityAnalyticsView: "analytics:view",
	}
	projected := make([]string, 0)
	for _, capability := range authz.EventClientCapabilityCeiling() {
		name, ok := sessionName[capability]
		if !assert.True(t, ok, "no session name for %s", capability) {
			continue
		}
		projected = append(projected, name)
	}
	sort.Strings(projected)
	assert.Equal(t, projected, applications.EventClientCapabilities())
}
