package authz

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// eventClientFixture wires a complete hook set for one user and one event.
// Every lookup can be overridden per test.
type eventClientFixture struct {
	user        *models.User
	event       *models.Event
	orgAllowed  bool
	orgRole     string
	orgErr      error
	granted     map[uuid.UUID]bool
	grantErr    error
	product     string
	productErr  error
	memberRoles map[uuid.UUID]string
	orgCalls    atomic.Int32
	grantCalls  atomic.Int32
	flagOn      bool
}

func newEventClientFixture() *eventClientFixture {
	clientID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	return &eventClientFixture{
		user:        &models.User{ID: uuid.Must(uuid.NewV4()), IsActive: true},
		event:       &models.Event{ID: eventID, ClientID: &clientID},
		granted:     map[uuid.UUID]bool{eventID: true},
		product:     "eventiapp",
		memberRoles: map[uuid.UUID]string{},
		flagOn:      true,
	}
}

func (f *eventClientFixture) install(t *testing.T) {
	t.Helper()
	previous := hooks
	t.Cleanup(func() { hooks = previous })
	configured := Hooks{
		SyncUser: func(string) (*models.User, error) {
			copy := *f.user
			return &copy, nil
		},
		GetEventByIDRaw: func(id uuid.UUID) (*models.Event, error) {
			if id != f.event.ID {
				other := uuid.Must(uuid.NewV4())
				return &models.Event{ID: id, ClientID: &other}, nil
			}
			copy := *f.event
			return &copy, nil
		},
		CheckAccessRecursiveE: func(uuid.UUID, uuid.UUID) (bool, string, error) {
			f.orgCalls.Add(1)
			return f.orgAllowed, f.orgRole, f.orgErr
		},
		ResolveClientProduct: func(uuid.UUID) (string, error) { return f.product, f.productErr },
		GetEventMemberRole: func(eventID, _ uuid.UUID) (string, bool, error) {
			role, ok := f.memberRoles[eventID]
			return role, ok, nil
		},
		GetEventMemberRoles: func(ids []uuid.UUID, _ uuid.UUID) (map[uuid.UUID]string, error) {
			result := map[uuid.UUID]string{}
			for _, id := range ids {
				if role, ok := f.memberRoles[id]; ok {
					result[id] = role
				}
			}
			return result, nil
		},
		GetClientByID: func(id uuid.UUID) (*models.Client, error) {
			return &models.Client{ID: id, Code: "eventiapp"}, nil
		},
	}
	if f.flagOn {
		configured.HasEventClientGrant = func(eventID, _ uuid.UUID) (bool, error) {
			f.grantCalls.Add(1)
			return f.granted[eventID], f.grantErr
		}
	}
	hooks = configured
}

func newEventRequest(tenant string) echo.Context {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/events/x", nil), httptest.NewRecorder())
	c.Set("cognito_sub", "sub")
	if tenant != "" {
		c.Set("tenant_code", tenant)
	}
	return c
}

func requireStatus(t *testing.T, err error, status int) {
	t.Helper()
	require.Error(t, err)
	var failure *Failure
	require.True(t, errors.As(err, &failure), "expected *Failure, got %T", err)
	assert.Equal(t, status, failure.Status)
}

func TestEventClientGrantAllowsEventManagerPowersOnly(t *testing.T) {
	f := newEventClientFixture()
	f.install(t)
	allowed := []Capability{CapabilityView, CapabilityEventManage, CapabilityGuestManage, CapabilityCheckin, CapabilityAnalyticsView}
	denied := []Capability{CapabilityEventCreate, CapabilityEventDelete, CapabilityMembersManage, CapabilityOrgManage, CapabilityEventClientsManage, CapabilityMetricsView}
	for _, capability := range allowed {
		_, _, err := RequireEventCapability(newEventRequest(""), f.event.ID, capability)
		assert.NoError(t, err, capability)
	}
	for _, capability := range denied {
		_, _, err := RequireEventCapability(newEventRequest(""), f.event.ID, capability)
		requireStatus(t, err, http.StatusForbidden)
		assert.Contains(t, err.Error(), "event client access", capability)
	}
}

func TestEventClientGrantIgnoresEventMemberNarrowing(t *testing.T) {
	f := newEventClientFixture()
	f.memberRoles[f.event.ID] = "VIEWER"
	f.install(t)
	_, _, err := RequireEventCapability(newEventRequest(""), f.event.ID, CapabilityEventManage)
	assert.NoError(t, err)
}

func TestOrganizationMemberHoldingGrantIsNeverElevated(t *testing.T) {
	f := newEventClientFixture()
	f.orgAllowed, f.orgRole = true, "VIEWER"
	f.install(t)
	_, _, err := RequireEventCapability(newEventRequest(""), f.event.ID, CapabilityEventManage)
	requireStatus(t, err, http.StatusForbidden)
	assert.Contains(t, err.Error(), "organization role")
	assert.Zero(t, f.grantCalls.Load(), "an organization member never reaches the grant path")
}

func TestOrganizationLookupErrorFailsClosedWithoutGrantLookup(t *testing.T) {
	f := newEventClientFixture()
	f.orgErr = errors.New("db down")
	f.install(t)
	_, _, err := RequireEventCapability(newEventRequest(""), f.event.ID, CapabilityView)
	requireStatus(t, err, http.StatusInternalServerError)
	assert.Zero(t, f.grantCalls.Load())
}

func TestGrantLookupAndProductErrorsFailClosed(t *testing.T) {
	f := newEventClientFixture()
	f.grantErr = errors.New("db down")
	f.install(t)
	_, _, err := RequireEventAccess(newEventRequest(""), f.event.ID)
	requireStatus(t, err, http.StatusInternalServerError)

	g := newEventClientFixture()
	g.productErr = errors.New("db down")
	g.install(t)
	_, _, err = RequireEventAccess(newEventRequest(""), g.event.ID)
	requireStatus(t, err, http.StatusInternalServerError)
}

func TestGrantDeniedOutsideEventiAppProducts(t *testing.T) {
	for _, product := range []string{"cafettonhouse", "itbem", ""} {
		f := newEventClientFixture()
		f.product = product
		f.install(t)
		_, _, err := RequireEventAccess(newEventRequest(""), f.event.ID)
		requireStatus(t, err, http.StatusForbidden)
	}
}

func TestGrantDeniedWhenFlagOffOrBrandedTenant(t *testing.T) {
	f := newEventClientFixture()
	f.flagOn = false
	f.install(t)
	_, _, err := RequireEventAccess(newEventRequest(""), f.event.ID)
	requireStatus(t, err, http.StatusForbidden)

	g := newEventClientFixture()
	g.install(t)
	_, _, err = RequireEventAccess(newEventRequest("cafettonhouse"), g.event.ID)
	requireStatus(t, err, http.StatusForbidden)
	assert.Zero(t, g.grantCalls.Load())
}

func TestFormerMemberGrantIsVoidBecauseLookupExcludesIt(t *testing.T) {
	// HasActiveGrant excludes any client_members row in the grant's org; the
	// hook therefore answers false and the caller is denied.
	f := newEventClientFixture()
	f.granted = map[uuid.UUID]bool{}
	f.install(t)
	_, _, err := RequireEventAccess(newEventRequest(""), f.event.ID)
	requireStatus(t, err, http.StatusForbidden)
}

func TestEventClientCannotReachAnotherEvent(t *testing.T) {
	f := newEventClientFixture()
	f.install(t)
	otherEvent := uuid.Must(uuid.NewV4())
	_, _, err := RequireEventAccess(newEventRequest(""), otherEvent)
	requireStatus(t, err, http.StatusForbidden)

	// Same organization, different event: the grant opens exactly one event.
	sameOrgEvent := &models.Event{ID: uuid.Must(uuid.NewV4()), ClientID: f.event.ClientID}
	err = EnsureEventAccess(f.user, sameOrgEvent)
	requireStatus(t, err, http.StatusForbidden)
}

func TestEnsureEventIDAccessHonoursGrants(t *testing.T) {
	f := newEventClientFixture()
	f.install(t)
	_, err := EnsureEventIDAccess(f.user, f.event.ID)
	assert.NoError(t, err)
}

func TestGrantNeverSatisfiesOrganizationChecks(t *testing.T) {
	f := newEventClientFixture()
	f.install(t)
	requireStatus(t, RequireClientAccess(f.user, *f.event.ClientID), http.StatusForbidden)
	requireStatus(t, RequireClientCapability(f.user, *f.event.ClientID, CapabilityEventManage), http.StatusForbidden)
	requireStatus(t, RequireEventClientForCreate(f.user, f.event.ClientID), http.StatusForbidden)
}

func TestCapabilityChecksShareOneOrganizationLookupPerRequest(t *testing.T) {
	f := newEventClientFixture()
	f.orgAllowed, f.orgRole = true, "EVENT_MANAGER"
	f.install(t)
	c := newEventRequest("")
	for _, capability := range []Capability{CapabilityEventManage, CapabilityEventDelete, CapabilityGuestManage, CapabilityCheckin, CapabilityAnalyticsView, CapabilityMembersManage, CapabilityEventClientsManage} {
		_, _, _ = RequireEventCapability(c, f.event.ID, capability)
	}
	assert.Equal(t, int32(1), f.orgCalls.Load())
}

func TestEventClientsManageMatrix(t *testing.T) {
	for _, tt := range []struct {
		role    string
		allowed bool
	}{
		{"Owner", true}, {"Admin", true}, {"INHERITED_OWNER", true}, {"INHERITED_ADMIN", true},
		{"EVENT_MANAGER", true}, {"EDITOR", false}, {"CHECKIN", false}, {"ANALYST", false}, {"MEMBER", false}, {"Guest", false},
	} {
		assert.Equal(t, tt.allowed, roleHasCapability(tt.role, CapabilityEventClientsManage), tt.role)
	}
	assert.True(t, platformHasCapability(&models.User{RootLevel: models.RootLevelPrimary}, CapabilityEventClientsManage))
	assert.False(t, platformHasCapability(&models.User{RootLevel: models.RootLevelOperational}, CapabilityEventClientsManage))

	f := newEventClientFixture()
	f.orgAllowed, f.orgRole = true, "EVENT_MANAGER"
	f.memberRoles[f.event.ID] = "EDITOR"
	f.install(t)
	_, _, err := RequireEventCapability(newEventRequest(""), f.event.ID, CapabilityEventClientsManage)
	requireStatus(t, err, http.StatusForbidden)
}

func TestMoveRequiresOrganizationManageOnBothSides(t *testing.T) {
	source := uuid.Must(uuid.NewV4())
	destination := uuid.Must(uuid.NewV4())
	user := &models.User{ID: uuid.Must(uuid.NewV4())}
	roles := map[uuid.UUID]string{}
	previous := hooks
	t.Cleanup(func() { hooks = previous })
	hooks = Hooks{CheckAccessRecursiveE: func(_ uuid.UUID, clientID uuid.UUID) (bool, string, error) {
		role, ok := roles[clientID]
		return ok, role, nil
	}}

	// Mixed user: event client in the source org (no membership) plus
	// EVENT_MANAGER of the destination.
	roles[destination] = "EVENT_MANAGER"
	requireStatus(t, RequireClientMoveAccess(user, &source, &destination), http.StatusForbidden)

	// EDITOR of both organizations keeps today's behaviour.
	roles[source], roles[destination] = "EDITOR", "EDITOR"
	assert.NoError(t, RequireClientMoveAccess(user, &source, &destination))

	// Same organization is not a move.
	assert.NoError(t, RequireClientMoveAccess(user, &source, &source))

	// Root 1 may move.
	assert.NoError(t, RequireClientMoveAccess(&models.User{RootLevel: models.RootLevelPrimary}, &source, &destination))
}

func TestDetachIsPrimaryRootOnly(t *testing.T) {
	source := uuid.Must(uuid.NewV4())
	for _, user := range []*models.User{
		{ID: uuid.Must(uuid.NewV4())},
		{RootLevel: models.RootLevelOperational},
	} {
		err := RequireClientMoveAccess(user, &source, nil)
		requireStatus(t, err, http.StatusForbidden)
		assert.Contains(t, err.Error(), "primary platform administrator")
	}
	assert.NoError(t, RequireClientMoveAccess(&models.User{RootLevel: models.RootLevelPrimary}, &source, nil))
	assert.NoError(t, RequireClientMoveAccess(&models.User{}, nil, nil))
}

func TestEventCapabilityForOrganizationEventsBatches(t *testing.T) {
	f := newEventClientFixture()
	f.orgAllowed, f.orgRole = true, "EVENT_MANAGER"
	narrowed := uuid.Must(uuid.NewV4())
	plain := uuid.Must(uuid.NewV4())
	f.memberRoles[narrowed] = "VIEWER"
	f.install(t)
	batchCalls := 0
	inner := hooks.GetEventMemberRoles
	hooks.GetEventMemberRoles = func(ids []uuid.UUID, user uuid.UUID) (map[uuid.UUID]string, error) {
		batchCalls++
		return inner(ids, user)
	}
	result, err := EventCapabilityForOrganizationEvents(f.user, *f.event.ClientID, []uuid.UUID{narrowed, plain}, CapabilityEventClientsManage)
	require.NoError(t, err)
	assert.False(t, result[narrowed])
	assert.True(t, result[plain])
	assert.Equal(t, int32(1), f.orgCalls.Load())
	assert.Equal(t, 1, batchCalls)
	assert.Zero(t, f.grantCalls.Load(), "the batch never honours grants")

	f.orgErr = errors.New("db down")
	_, err = EventCapabilityForOrganizationEvents(f.user, *f.event.ClientID, []uuid.UUID{plain}, CapabilityEventClientsManage)
	requireStatus(t, err, http.StatusInternalServerError)
}

func TestEventClientCeilingIsStable(t *testing.T) {
	assert.ElementsMatch(t, []Capability{CapabilityView, CapabilityEventManage, CapabilityGuestManage, CapabilityCheckin, CapabilityAnalyticsView}, EventClientCapabilityCeiling())
}
