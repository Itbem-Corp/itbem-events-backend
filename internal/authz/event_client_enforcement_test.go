package authz

import (
	"errors"
	"net/http"
	"testing"

	"events-stocks/internal/capabilities"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureGrantOnly turns the enforcement setup into an event client: no
// organization membership, an active grant, and an EventiApp organization.
func configureGrantOnly(setup *enforcementSetup) {
	hooks.CheckAccessRecursiveE = func(uuid.UUID, uuid.UUID) (bool, string, error) {
		setup.eCalls.Add(1)
		return false, "", nil
	}
	setup.access = ApplicationAccess{}
	hooks.HasEventClientGrant = func(uuid.UUID, uuid.UUID) (bool, error) { return true, nil }
	hooks.ResolveClientProduct = func(uuid.UUID) (string, error) { return "eventiapp", nil }
}

func policyWithout(names ...string) fakeCeiling {
	allowed := map[string]struct{}{}
	for _, name := range capabilities.PolicyAllowList("eventiapp") {
		allowed[name] = struct{}{}
	}
	for _, name := range names {
		delete(allowed, name)
	}
	return fakeCeiling{code: "eventiapp", restricted: true, names: allowed}
}

func TestEventClientGrantPathAppliesThePolicyCeiling(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeEnforce)
	configureGrantOnly(setup)
	session := policyWithout("guests:manage")

	_, _, err := RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityView)
	require.NoError(t, err, "the policy keeps events:view")

	_, _, err = RequireEventCapability(newEnforcementRequest(session), setup.eventID, CapabilityGuestManage)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err))
	assert.Contains(t, err.Error(), "application access policy")

	_, _, err = RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err, "no policy, no ceiling")
}

func TestEventClientGrantPathCeilingShadowNeverDenies(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeOff, capabilities.ModeShadow)
	configureGrantOnly(setup)
	logs := captureLogs(t)

	_, _, err := RequireEventCapability(newEnforcementRequest(policyWithout("guests:manage")), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err)
	assert.Contains(t, logs.String(), "authz shadow denial")
}

func TestEventClientGrantStaysCappedUnderEntitlementEnforce(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	configureGrantOnly(setup)

	_, _, err := RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityGuestManage)
	require.NoError(t, err, "a grant on an EventiApp organization is entitled")
	_, _, err = RequireEventCapability(newEnforcementRequest(nil), setup.eventID, CapabilityEventDelete)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event client access")
}

func TestNonEntitledMemberIsDeniedEventReadsInEnforceOnly(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	// A grant must never rescue a non-entitled member: the organization path
	// decides and the grant path is not consulted.
	grantLookups := 0
	hooks.HasEventClientGrant = func(uuid.UUID, uuid.UUID) (bool, error) { grantLookups++; return true, nil }
	hooks.ResolveClientProduct = func(uuid.UUID) (string, error) { return "eventiapp", nil }

	_, _, err := RequireEventAccess(newEnforcementRequest(nil), setup.eventID)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err))
	assert.Contains(t, err.Error(), "not enabled for this application")
	assert.Zero(t, grantLookups)

	user := setup.user
	err = EnsureEventAccess(&user, &models.Event{ID: setup.eventID, ClientID: &setup.clientID})
	require.Error(t, err)

	restore := ConfigureEnforcement(capabilities.ModeShadow, capabilities.ModeOff)
	t.Cleanup(restore)
	_, _, err = RequireEventAccess(newEnforcementRequest(nil), setup.eventID)
	require.NoError(t, err, "shadow logs but allows")
}

func TestOrganizationEventBatchAppliesEntitlementAndFailsClosed(t *testing.T) {
	setup := newEnforcementSetup(t, capabilities.ModeEnforce, capabilities.ModeOff)
	hooks.GetEventMemberRoles = func([]uuid.UUID, uuid.UUID) (map[uuid.UUID]string, error) {
		return map[uuid.UUID]string{}, nil
	}
	user := setup.user
	ids := []uuid.UUID{setup.eventID}

	result, err := EventCapabilityForOrganizationEvents(&user, setup.clientID, ids, CapabilityEventClientsManage)
	require.NoError(t, err)
	assert.False(t, result[setup.eventID], "not entitled in enforce")

	setup.access = ApplicationAccess{Allowed: true, Role: "EVENT_MANAGER", Entitled: true}
	result, err = EventCapabilityForOrganizationEvents(&user, setup.clientID, ids, CapabilityEventClientsManage)
	require.NoError(t, err)
	assert.True(t, result[setup.eventID])

	setup.accessErr = errors.New("connection reset")
	_, err = EventCapabilityForOrganizationEvents(&user, setup.clientID, ids, CapabilityEventClientsManage)
	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, failureStatus(t, err))
}
