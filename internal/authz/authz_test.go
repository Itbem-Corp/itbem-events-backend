package authz

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireEventAccessLoadsUserAndEventConcurrently(t *testing.T) {
	e := echo.New()
	c := e.NewContext(
		httptest.NewRequest(http.MethodGet, "/api/events/event-id/detail", nil),
		httptest.NewRecorder(),
	)
	c.Set("cognito_sub", "cognito-sub")

	userID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	started := make(chan string, 2)
	release := make(chan struct{})

	restore := ReplaceHooksForTest(Hooks{
		SyncUser: func(cognitoSub string) (*models.User, error) {
			assert.Equal(t, "cognito-sub", cognitoSub)
			started <- "user"
			<-release
			return &models.User{ID: userID, IsRoot: true}, nil
		},
		GetEventByIDRaw: func(gotEventID uuid.UUID) (*models.Event, error) {
			assert.Equal(t, eventID, gotEventID)
			started <- "event"
			<-release
			return &models.Event{ID: eventID}, nil
		},
	})
	t.Cleanup(restore)

	type accessResult struct {
		user  *models.User
		event *models.Event
		err   error
	}
	done := make(chan accessResult, 1)
	go func() {
		user, event, err := RequireEventAccess(c, eventID)
		done <- accessResult{user: user, event: event, err: err}
	}()

	seen := map[string]bool{}
	for range 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("user sync and event lookup did not both start before either completed")
		}
	}
	close(release)

	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.NotNil(t, result.user)
		require.NotNil(t, result.event)
		assert.Equal(t, userID, result.user.ID)
		assert.Equal(t, eventID, result.event.ID)
	case <-time.After(time.Second):
		t.Fatal("event access check did not finish")
	}
	assert.Equal(t, map[string]bool{"user": true, "event": true}, seen)
}

func TestOrganizationRoleCapabilitiesAreLeastPrivilege(t *testing.T) {
	tests := []struct {
		role       string
		capability Capability
		allowed    bool
	}{
		{"Owner", CapabilityOrgManage, true},
		{"Admin", CapabilityMembersManage, true},
		{"EVENT_MANAGER", CapabilityEventManage, true},
		{"EDITOR", CapabilityGuestManage, true},
		{"CHECKIN", CapabilityCheckin, true},
		{"CHECKIN", CapabilityGuestManage, false},
		{"ANALYST", CapabilityAnalyticsView, true},
		{"ANALYST", CapabilityEventManage, false},
		{"Guest", CapabilityView, true},
		{"Guest", CapabilityCheckin, false},
		{"INHERITED_ADMIN", CapabilityMembersManage, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.allowed, roleHasCapability(tt.role, tt.capability), "%s / %s", tt.role, tt.capability)
	}
}

func TestEventAssignmentCanNarrowButNeverElevateOrganizationPermissions(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPut, "/api/events/event-id", nil), httptest.NewRecorder())
	c.Set("cognito_sub", "event-member")
	userID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	memberRole := "VIEWER"
	restore := ReplaceHooksForTest(Hooks{
		SyncUser:             func(string) (*models.User, error) { return &models.User{ID: userID}, nil },
		GetEventByIDRaw:      func(uuid.UUID) (*models.Event, error) { return &models.Event{ID: eventID, ClientID: &clientID}, nil },
		CheckAccessRecursive: func(uuid.UUID, uuid.UUID) (bool, string) { return true, "Admin" },
		GetEventMemberRole:   func(uuid.UUID, uuid.UUID) (string, bool, error) { return memberRole, true, nil },
	})
	t.Cleanup(restore)

	_, _, err := RequireEventCapability(c, eventID, CapabilityEventManage)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event assignment")

	memberRole = "EDITOR"
	_, _, err = RequireEventCapability(c, eventID, CapabilityEventManage)
	require.NoError(t, err)

	memberRole = "EVENT_MANAGER"
	// The assignment cannot elevate a Viewer organization role.
	hooks.CheckAccessRecursive = func(uuid.UUID, uuid.UUID) (bool, string) { return true, "Guest" }
	_, _, err = RequireEventCapability(c, eventID, CapabilityEventManage)
	require.Error(t, err)
}

func TestOperationalRootHasSupportCapabilitiesButNotGovernanceCapabilities(t *testing.T) {
	operationalRoot := &models.User{RootLevel: models.RootLevelOperational}

	assert.True(t, platformHasCapability(operationalRoot, CapabilityView))
	assert.True(t, platformHasCapability(operationalRoot, CapabilityGuestManage))
	assert.True(t, platformHasCapability(operationalRoot, CapabilityCheckin))
	assert.True(t, platformHasCapability(operationalRoot, CapabilityAnalyticsView))
	assert.False(t, platformHasCapability(operationalRoot, CapabilityEventManage))
	assert.False(t, platformHasCapability(operationalRoot, CapabilityMembersManage))
	assert.False(t, platformHasCapability(operationalRoot, CapabilityOrgManage))
}

func TestOperationalRootEventCapabilityCeiling(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodPut, "/api/events/event-id", nil), httptest.NewRecorder())
	c.Set("cognito_sub", "operational-root")
	eventID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	restore := ReplaceHooksForTest(Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: uuid.Must(uuid.NewV4()), RootLevel: models.RootLevelOperational}, nil
		},
		GetEventByIDRaw: func(uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: eventID, ClientID: &clientID}, nil
		},
	})
	t.Cleanup(restore)

	_, _, err := RequireEventCapability(c, eventID, CapabilityGuestManage)
	require.NoError(t, err)

	_, _, err = RequireEventCapability(c, eventID, CapabilityEventManage)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform administrator level")
}

func TestRequireEventAccessRejectsMissingIdentityBeforeEventLookup(t *testing.T) {
	e := echo.New()
	c := e.NewContext(
		httptest.NewRequest(http.MethodGet, "/api/events/event-id/detail", nil),
		httptest.NewRecorder(),
	)

	eventLookupCalled := false
	restore := ReplaceHooksForTest(Hooks{
		GetEventByIDRaw: func(eventID uuid.UUID) (*models.Event, error) {
			eventLookupCalled = true
			return &models.Event{ID: eventID}, nil
		},
	})
	t.Cleanup(restore)

	user, event, err := RequireEventAccess(c, uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Nil(t, user)
	assert.Nil(t, event)
	assert.False(t, eventLookupCalled)
	failure, ok := err.(*Failure)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, failure.Status)
}

func TestRequireEventAccessPreservesIdentityErrorPriority(t *testing.T) {
	e := echo.New()
	c := e.NewContext(
		httptest.NewRequest(http.MethodGet, "/api/events/event-id/detail", nil),
		httptest.NewRecorder(),
	)
	c.Set("cognito_sub", "missing-user")

	restore := ReplaceHooksForTest(Hooks{
		SyncUser: func(string) (*models.User, error) {
			return nil, errors.New("identity unavailable")
		},
		GetEventByIDRaw: func(uuid.UUID) (*models.Event, error) {
			return nil, errors.New("database unavailable")
		},
	})
	t.Cleanup(restore)

	user, event, err := RequireEventAccess(c, uuid.Must(uuid.NewV4()))

	require.Error(t, err)
	assert.Nil(t, user)
	assert.Nil(t, event)
	failure, ok := err.(*Failure)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, failure.Status)
	assert.Equal(t, "identity unavailable", failure.Detail)
}

func TestRequireEventAccessStillChecksClientMembershipForNonRootUser(t *testing.T) {
	e := echo.New()
	c := e.NewContext(
		httptest.NewRequest(http.MethodGet, "/api/events/event-id/detail", nil),
		httptest.NewRecorder(),
	)
	c.Set("cognito_sub", "member-user")

	userID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	accessChecks := 0
	restore := ReplaceHooksForTest(Hooks{
		SyncUser: func(string) (*models.User, error) {
			return &models.User{ID: userID}, nil
		},
		GetEventByIDRaw: func(uuid.UUID) (*models.Event, error) {
			return &models.Event{ID: eventID, ClientID: &clientID}, nil
		},
		CheckAccessRecursive: func(gotUserID, gotClientID uuid.UUID) (bool, string) {
			accessChecks++
			assert.Equal(t, userID, gotUserID)
			assert.Equal(t, clientID, gotClientID)
			return true, "Admin"
		},
	})
	t.Cleanup(restore)

	user, event, err := RequireEventAccess(c, eventID)

	require.NoError(t, err)
	require.NotNil(t, user)
	require.NotNil(t, event)
	assert.Equal(t, 1, accessChecks)
}

func TestLoadEventAccessInputsRelaysWorkerPanicToCaller(t *testing.T) {
	assert.PanicsWithValue(t, "identity loader panic", func() {
		_, _, _ = loadEventAccessInputs(
			func() (*models.User, error) { panic("identity loader panic") },
			func() (*models.Event, error) { return &models.Event{}, nil },
		)
	})
}

func TestLoadEventAccessInputsKeepsIdentityErrorAheadOfEventPanic(t *testing.T) {
	identityErr := errors.New("identity unavailable")

	assert.NotPanics(t, func() {
		user, event, err := loadEventAccessInputs(
			func() (*models.User, error) { return nil, identityErr },
			func() (*models.Event, error) { panic("event loader panic") },
		)

		assert.ErrorIs(t, err, identityErr)
		assert.Nil(t, user)
		assert.Nil(t, event)
	})
}

func TestLoadEventAccessInputsDoesNotWaitForEventAfterIdentityError(t *testing.T) {
	identityErr := errors.New("identity unavailable")
	eventStarted := make(chan struct{})
	releaseEvent := make(chan struct{})
	eventStopped := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, _, err := loadEventAccessInputs(
			func() (*models.User, error) { return nil, identityErr },
			func() (*models.Event, error) {
				defer close(eventStopped)
				close(eventStarted)
				<-releaseEvent
				return nil, nil
			},
		)
		done <- err
	}()

	select {
	case <-eventStarted:
	case <-time.After(time.Second):
		close(releaseEvent)
		<-done
		t.Fatal("event loader did not start")
	}

	select {
	case err := <-done:
		assert.ErrorIs(t, err, identityErr)
	case <-time.After(time.Second):
		close(releaseEvent)
		<-eventStopped
		<-done
		t.Fatal("identity error waited for the blocked event loader")
	}

	close(releaseEvent)
	select {
	case <-eventStopped:
	case <-time.After(time.Second):
		t.Fatal("event loader did not stop after release")
	}
}
