package authz

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"events-stocks/internal/capabilities"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyRoleHasCapability is a frozen copy of the switch that the
// internal/capabilities table replaced.
func legacyRoleHasCapability(role string, capability Capability) bool {
	code := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(role)), "INHERITED_")
	if code == "OWNER" || code == "ADMIN" {
		return true
	}
	switch capability {
	case CapabilityView:
		return code != ""
	case CapabilityEventCreate:
		return code == "EVENT_MANAGER"
	case CapabilityEventManage:
		return code == "EVENT_MANAGER" || code == "EDITOR"
	case CapabilityEventDelete:
		return false
	case CapabilityGuestManage:
		return code == "EVENT_MANAGER" || code == "EDITOR" || code == "MEMBER"
	case CapabilityCheckin:
		return code == "EVENT_MANAGER" || code == "CHECKIN"
	case CapabilityAnalyticsView:
		return code == "EVENT_MANAGER" || code == "EDITOR" || code == "ANALYST"
	default:
		return false
	}
}

var parityRoles = []string{
	"OWNER", "Owner", "ADMIN", "INHERITED_OWNER", "INHERITED_ADMIN", "inherited_admin",
	"EVENT_MANAGER", "EDITOR", "MEMBER", "CHECKIN", "ANALYST", "GUEST", "VIEWER",
	"LEGACY_X", "", " ",
}

func TestRoleDecisionsAreIdenticalToTheLegacySwitch(t *testing.T) {
	legacyCapabilities := []Capability{
		CapabilityView, CapabilityEventCreate, CapabilityEventManage, CapabilityEventDelete,
		CapabilityGuestManage, CapabilityCheckin, CapabilityAnalyticsView, CapabilityMembersManage,
		CapabilityOrgManage, CapabilityMetricsView,
	}
	for _, role := range parityRoles {
		for _, capability := range legacyCapabilities {
			assert.Equal(t, legacyRoleHasCapability(role, capability), roleHasCapability(role, capability), "%q / %s", role, capability)
		}
	}
	// The one addition: event-client management for Event manager and Owner/Admin.
	for _, role := range parityRoles {
		code := capabilities.NormalizeRole(role)
		expected := code == "EVENT_MANAGER" || code == "OWNER" || code == "ADMIN"
		assert.Equal(t, expected, roleHasCapability(role, CapabilityEventClientsManage), role)
	}
}

type enforcementSetup struct {
	userID      uuid.UUID
	clientID    uuid.UUID
	eventID     uuid.UUID
	user        models.User
	role        string
	access      ApplicationAccess
	accessErr   error
	legacyCalls atomic.Int32
	eCalls      atomic.Int32
	appCalls    atomic.Int32
}

func newEnforcementSetup(t *testing.T, entitlement, ceiling capabilities.Mode) *enforcementSetup {
	t.Helper()
	setup := &enforcementSetup{
		userID:   uuid.Must(uuid.NewV4()),
		clientID: uuid.Must(uuid.NewV4()),
		eventID:  uuid.Must(uuid.NewV4()),
		role:     "EVENT_MANAGER",
	}
	setup.user = models.User{ID: setup.userID, IsActive: true}
	setup.access = ApplicationAccess{Allowed: true, Role: "EVENT_MANAGER", Entitled: false, Reason: "member_not_entitled"}
	previous := hooks
	hooks = Hooks{
		SyncUser: func(string) (*models.User, error) {
			user := setup.user
			return &user, nil
		},
		GetEventByIDRaw: func(uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: setup.eventID, ClientID: &setup.clientID}, nil
		},
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) {
			setup.legacyCalls.Add(1)
			return true, setup.role
		},
		CheckAccessRecursiveE: func(uuid.UUID, uuid.UUID) (bool, string, error) {
			setup.eCalls.Add(1)
			if setup.accessErr != nil {
				return false, "", setup.accessErr
			}
			return true, setup.role, nil
		},
		CheckApplicationAccessRecursive: func(uuid.UUID, uuid.UUID, string) (ApplicationAccess, error) {
			setup.appCalls.Add(1)
			if setup.accessErr != nil {
				return ApplicationAccess{}, setup.accessErr
			}
			return setup.access, nil
		},
	}
	restoreModes := ConfigureEnforcement(entitlement, ceiling)
	t.Cleanup(func() {
		hooks = previous
		restoreModes()
	})
	return setup
}

type fakeCeiling struct {
	code       string
	restricted bool
	names      map[string]struct{}
}

func (f fakeCeiling) ApplicationCode() string { return f.code }
func (f fakeCeiling) PolicyCeiling() (map[string]struct{}, bool) {
	return f.names, f.restricted
}

func newEnforcementRequest(session PolicyCeilingSource) echo.Context {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/events/x", nil), httptest.NewRecorder())
	c.SetPath("/api/events/:id")
	c.Set("cognito_sub", "sub")
	c.Set("tenant_code", "eventiapp")
	if session != nil {
		c.Set(ApplicationSessionContextKey, session)
	}
	return c
}

func failureStatus(t *testing.T, err error) int {
	t.Helper()
	var failure *Failure
	require.True(t, errors.As(err, &failure), "expected *Failure, got %v", err)
	return failure.Status
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buffer, nil)))
	previousDedupe := shadowLog
	shadowLog = newShadowDeduper(shadowDedupeTTL, shadowDedupeEntries, time.Now)
	t.Cleanup(func() {
		slog.SetDefault(previous)
		shadowLog = previousDedupe
	})
	return buffer
}

func TestEntitlementOffIsIdenticalToTheOrganizationLookup(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeOff)
	for _, role := range parityRoles {
		setup.role = role
		for _, capability := range capabilities.All() {
			_, _, err := RequireEventCapability(newEnforcementRequest(nil), setup.eventID, capability)
			expected := strings.TrimSpace(role) != "" && roleHasCapability(role, capability)
			assert.Equal(t, expected, err == nil, "%q / %s: %v", role, capability, err)
		}
	}
	assert.Zero(t, setup.appCalls.Load(), "off never runs the entitlement query")
	assert.Zero(t, setup.legacyCalls.Load(), "the error-returning hook is preferred")
}

func TestOrganizationLookupErrorIsA500ThatNeverFallsThrough(t *testing.T) {
	for _, mode := range []capabilities.Mode{capabilities.ModeOff, capabilities.ModeShadow, capabilities.ModeEnforce} {
		setup := newEnforcementSetup(t, mode, capabilities.ModeOff)
		setup.accessErr = errors.New("connection reset")
		_, _, err := RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityView)
		require.Error(t, err, mode)
		assert.Equal(t, http.StatusInternalServerError, failureStatus(t, err), mode)
		assert.NotContains(t, err.Error(), "connection reset", "database errors are logged, not returned")

		user := setup.user
		err = RequireClientCapability(&user, setup.clientID, CapabilityMembersManage)
		assert.Equal(t, http.StatusInternalServerError, failureStatus(t, err), mode)
		assert.Zero(t, setup.legacyCalls.Load(), "the swallowing legacy hook is never consulted")
	}
}

func TestEntitlementShadowAllowsAndLogsOnceWithoutIdentifiers(t *testing.T) {
	logs := captureLogs(t)
	setup := newEnforcementSetup(t, capabilities.ModeShadow, capabilities.ModeOff)
	for i := 0; i < 3; i++ {
		_, _, err := RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityGuestManage)
		require.NoError(t, err)
	}
	output := logs.String()
	// One line per (user, organization, capability): the event read (view)
	// and the guest action, never repeated on the next two requests.
	assert.Equal(t, 2, strings.Count(output, "authz shadow denial"), output)
	assert.Contains(t, output, "check=entitlement")
	assert.Contains(t, output, "reason=member_not_entitled")
	assert.Contains(t, output, "capability=guest:manage")
	assert.Contains(t, output, "route=/api/events/:id")
	assert.Contains(t, output, "tenant=eventiapp")
	assert.NotContains(t, output, setup.userID.String())
	assert.NotContains(t, output, setup.clientID.String())
}

func TestEntitlementEnforceDeniesEveryEventScopedCheck(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	for _, reason := range []string{"member_not_entitled", "product_mismatch", "organization_inactive", "product_disabled"} {
		setup.access = ApplicationAccess{Allowed: true, Role: "INHERITED_OWNER", Reason: reason}

		_, _, err := RequireEventAccess(newEnforcementRequest(nil), setup.eventID)
		require.Error(t, err, reason)
		assert.Equal(t, http.StatusForbidden, failureStatus(t, err))
		assert.Contains(t, err.Error(), "not enabled for this application")

		_, _, err = RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityView)
		assert.Equal(t, http.StatusForbidden, failureStatus(t, err), reason)

		user := setup.user
		assert.Equal(t, http.StatusForbidden, failureStatus(t, RequireEventListAccess(&user, setup.clientID)), reason)
		assert.Equal(t, http.StatusForbidden, failureStatus(t, RequireEventClientForCreate(&user, &setup.clientID)), reason)
	}
}

func TestEntitlementEnforceNeverBlocksOrganizationAndMemberManagement(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	setup.role = "OWNER"
	setup.access = ApplicationAccess{Allowed: true, Role: "OWNER", Reason: "member_not_entitled"}
	user := setup.user
	require.NoError(t, RequireClientAccess(&user, setup.clientID))
	require.NoError(t, RequireClientCapability(&user, setup.clientID, CapabilityMembersManage), "an Owner must be able to repair access")
	require.NoError(t, RequireClientCapability(&user, setup.clientID, CapabilityOrgManage))
	assert.Zero(t, setup.appCalls.Load(), "organization scope never runs the entitlement query")
}

func TestEntitlementEnforceAllowsEntitledMembersAndRoots(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	setup.access = ApplicationAccess{Allowed: true, Role: "EVENT_MANAGER", Entitled: true}
	_, _, err := RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err)

	setup.access = ApplicationAccess{Allowed: true, Role: "OWNER", Reason: "product_mismatch"}
	setup.user = models.User{ID: setup.userID, IsActive: true, IsRoot: true, RootLevel: models.RootLevelPrimary}
	before := setup.appCalls.Load()
	_, _, err = RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityEventDelete)
	require.NoError(t, err, "platform roots bypass entitlement on EventiApp")
	assert.Equal(t, before, setup.appCalls.Load())
}

func TestEntitlementEnforceWithoutTheHookFailsClosed(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	hooks.CheckApplicationAccessRecursive = nil
	_, _, err := RequireEventAccess(newEnforcementRequest(nil), setup.eventID)
	assert.Equal(t, http.StatusInternalServerError, failureStatus(t, err))

	ConfigureEnforcement(capabilities.ModeShadow, capabilities.ModeOff)
	_, _, err = RequireEventAccess(newEnforcementRequest(nil), setup.eventID)
	assert.NoError(t, err, "shadow never changes a decision")
}

func TestCapabilitiesEndpointPatternRunsOneAccessQueryPerRequest(t *testing.T) {
	for _, mode := range []capabilities.Mode{capabilities.ModeOff, capabilities.ModeEnforce} {
		setup := newEnforcementSetup(t, mode, capabilities.ModeOff)
		setup.access = ApplicationAccess{Allowed: true, Role: "EVENT_MANAGER", Entitled: true}
		c := newEnforcementRequest(nil)
		_, _, err := RequireEventAccess(c, setup.eventID)
		require.NoError(t, err)
		for _, capability := range []Capability{CapabilityEventManage, CapabilityEventDelete, CapabilityGuestManage, CapabilityCheckin, CapabilityAnalyticsView, CapabilityMembersManage} {
			_, _, _ = RequireEventCapability(c, setup.eventID, capability)
		}
		assert.Equal(t, int32(1), setup.eCalls.Load()+setup.appCalls.Load(), mode)
	}
}

func policySession(names ...string) fakeCeiling {
	allowed := map[string]struct{}{}
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return fakeCeiling{code: "eventiapp", restricted: true, names: allowed}
}

func TestPolicyCeilingOffAndShadowNeverChangeDecisions(t *testing.T) {
	logs := captureLogs(t)
	for _, mode := range []capabilities.Mode{capabilities.ModeOff, capabilities.ModeShadow} {
		setup := newEnforcementSetup(t, capabilities.ModeOff, mode)
		_, _, err := RequireEventCapability(newEnforcementRequest(policySession("events:view")), setup.eventID, CapabilityGuestManage)
		require.NoError(t, err, mode)
	}
	output := logs.String()
	assert.Equal(t, 1, strings.Count(output, "authz shadow denial"), output)
	assert.Contains(t, output, "check=policy")
	assert.Contains(t, output, "reason=policy_excludes")
}

func TestPolicyCeilingEnforceCapsAllowListedCapabilities(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeEnforce)
	session := policySession("events:view", "events:manage")

	_, _, err := RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityGuestManage)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err))
	assert.Contains(t, err.Error(), "application access policy")

	_, _, err = RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityEventManage)
	require.NoError(t, err)

	// No policy: unchanged.
	_, _, err = RequireEventCapability(newEnforcementRequest(fakeCeiling{code: "eventiapp"}), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err)
	_, _, err = RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err)
}

func TestPolicyCeilingNeverStripsEventDeleteOrThePrimaryRoot(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeEnforce)
	setup.role = "OWNER"
	session := policySession("events:view")
	_, _, err := RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityEventDelete)
	require.NoError(t, err, "events:delete is outside the allow-list")

	setup.user = models.User{ID: setup.userID, IsActive: true, IsRoot: true, RootLevel: models.RootLevelPrimary}
	_, _, err = RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err, "the primary root is exempt")
}

func TestPolicyCeilingCapsOperationalRoot(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeEnforce)
	setup.user = models.User{ID: setup.userID, IsActive: true, IsRoot: true, RootLevel: models.RootLevelOperational}
	session := policySession("events:view")
	_, _, err := RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityGuestManage)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err))

	user := setup.user
	stampRequestAuthorization(newEnforcementRequest(session), &user)
	err = RequireClientCapability(&user, setup.clientID, CapabilityMembersManage)
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err), "members:manage is allow-listed, so it is capped too")
}

func TestPolicyCeilingIgnoresOtherApplicationsAllowList(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeEnforce)
	session := fakeCeiling{code: "unknown-product", restricted: true, names: map[string]struct{}{}}
	_, _, err := RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err)
}

func TestShadowDeduperHonoursTTLAndCapacity(t *testing.T) {
	now := time.Unix(0, 0)
	deduper := newShadowDeduper(10*time.Minute, 2, func() time.Time { return now })
	assert.True(t, deduper.first("a"))
	assert.False(t, deduper.first("a"))
	now = now.Add(11 * time.Minute)
	assert.True(t, deduper.first("a"))
	assert.True(t, deduper.first("b"))
	assert.True(t, deduper.first("c"), "a full map is pruned or reset, never blocks logging")
	assert.LessOrEqual(t, len(deduper.seen), 2)
}

func TestConfigureEnforcementNormalizesUnknownModes(t *testing.T) {
	restore := ConfigureEnforcement(capabilities.Mode("on"), capabilities.Mode("ENFORCE"))
	t.Cleanup(restore)
	assert.Equal(t, capabilities.ModeOff, EntitlementMode())
	assert.Equal(t, capabilities.ModeOff, PolicyCeilingMode())
}
