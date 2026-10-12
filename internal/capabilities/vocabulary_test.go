package capabilities

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPackageImportsOnlyTheStandardLibrary(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").Output()
	require.NoError(t, err)
	var nonStandard []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line = strings.TrimSpace(line); line != "" && line != "events-stocks/internal/capabilities" {
			nonStandard = append(nonStandard, line)
		}
	}
	assert.Empty(t, nonStandard, "internal/capabilities must stay a leaf package")
}

func TestEveryCapabilityHasASessionName(t *testing.T) {
	seen := map[string]Capability{}
	for _, capability := range All() {
		name, ok := SessionName(capability)
		require.True(t, ok, capability)
		previous, duplicate := seen[name]
		assert.False(t, duplicate, "%s and %s share session name %s", previous, capability, name)
		seen[name] = capability
	}
	_, ok := SessionName(Capability("unknown"))
	assert.False(t, ok)
}

// sessionOnlyPolicyNames are allow-listed policy names with no authz
// capability: the session (and the surface gate) is their only enforcement.
var sessionOnlyPolicyNames = map[string]struct{}{
	"dashboard:view":      {},
	"audit:view":          {},
	"automation:view":     {},
	"automation:manage":   {},
	"platform:users:view": {},
	"organizations:view":  {},
}

func TestEveryAllowListedPolicyNameIsMappedOrDocumentedSessionOnly(t *testing.T) {
	projected := map[string]struct{}{}
	for _, capability := range All() {
		name, _ := SessionName(capability)
		projected[name] = struct{}{}
	}
	for _, application := range PolicyApplications() {
		for _, name := range PolicyAllowList(application) {
			_, mapped := projected[name]
			_, sessionOnly := sessionOnlyPolicyNames[name]
			assert.True(t, mapped || sessionOnly, "%s/%s is neither a capability nor a documented session-only name", application, name)
		}
	}
}

func TestPolicyAllowListKeepsDeleteAndClientManagementOutsideTheCeiling(t *testing.T) {
	for _, application := range PolicyApplications() {
		assert.False(t, PolicyAllows(application, "events:delete"), application)
		assert.False(t, PolicyAllows(application, "events:clients:manage"), application)
	}
	assert.True(t, PolicyAllows(" EventiApp ", "guests:manage"))
	assert.False(t, PolicyAllows("unknown", "guests:manage"))
	assert.Equal(t, []string{"cafettonhouse", "eventiapp", "itbem"}, PolicyApplications())
}

func TestRoleCapabilitiesKeepUnknownCodesAtViewOnly(t *testing.T) {
	assert.Equal(t, []Capability{View}, RoleCapabilities("LEGACY_X"))
	assert.Nil(t, RoleCapabilities(""))
	assert.Nil(t, RoleCapabilities("  "))
	assert.False(t, IsKnownRole("LEGACY_X"))
	assert.True(t, IsKnownRole("inherited_owner"))
	assert.True(t, IsKnownRole("Viewer"))
	assert.Equal(t, All(), RoleCapabilities("INHERITED_ADMIN"))
	assert.True(t, RoleHas("event_manager", EventClientsManage))
	assert.False(t, RoleHas("EDITOR", EventClientsManage))
}

func TestEventClientCapabilitiesExcludeOrganizationPowers(t *testing.T) {
	granted := EventClientCapabilities()
	for _, denied := range []Capability{EventCreate, EventDelete, MembersManage, OrgManage, EventClientsManage, MetricsView} {
		assert.NotContains(t, granted, denied)
	}
	assert.ElementsMatch(t, []Capability{View, EventManage, GuestManage, Checkin, AnalyticsView}, granted)
}

func TestParseMode(t *testing.T) {
	for raw, expected := range map[string]Mode{"": ModeOff, "off": ModeOff, " Shadow ": ModeShadow, "ENFORCE": ModeEnforce} {
		mode, err := ParseMode(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, expected, mode, raw)
	}
	mode, err := ParseMode("on")
	assert.Error(t, err)
	assert.Equal(t, ModeOff, mode, "an invalid value must fall back to off")
}
