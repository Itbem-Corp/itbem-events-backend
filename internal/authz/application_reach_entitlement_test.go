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

// The application reach (#297) and the product entitlement / policy ceiling
// (#288) are independent boundaries: a request must pass both, a deny from
// either stays 403 and a lookup error from either stays 500.
func TestApplicationReachAndEntitlementMustBothPass(t *testing.T) {
	restoreModes := ConfigureEnforcement(capabilities.ModeEnforce, capabilities.ModeOff)
	t.Cleanup(restoreModes)

	clientID := uuid.Must(uuid.NewV4())
	var reachable bool
	var reachErr error
	var entitled bool
	restore := ReplaceHooksForTest(Hooks{
		CheckAccessRecursiveE: func(uuid.UUID, uuid.UUID) (bool, string, error) { return true, "OWNER", nil },
		CheckApplicationAccessRecursive: func(uuid.UUID, uuid.UUID, string) (ApplicationAccess, error) {
			return ApplicationAccess{Allowed: true, Role: "OWNER", Entitled: entitled, Reason: "member_not_entitled"}, nil
		},
		ReachableClientIDsInApplication: func(_ uuid.UUID, app string, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
			assert.Equal(t, "eventiapp", app)
			if reachErr != nil {
				return nil, reachErr
			}
			return map[uuid.UUID]bool{ids[0]: reachable}, nil
		},
	})
	t.Cleanup(restore)

	newUser := func() *models.User {
		return &models.User{ID: uuid.Must(uuid.NewV4()), SessionApplicationCode: "eventiapp"}
	}
	statusOf := func(err error) int {
		t.Helper()
		var failure *Failure
		require.True(t, errors.As(err, &failure), "expected *Failure, got %v", err)
		return failure.Status
	}

	// Reachable and entitled: allowed on both paths.
	reachable, reachErr, entitled = true, nil, true
	require.NoError(t, RequireEventListAccess(newUser(), clientID))
	require.NoError(t, RequireEventClientForCreate(newUser(), &clientID))

	// Reachable but not entitled: the entitlement still denies.
	reachable, reachErr, entitled = true, nil, false
	err := RequireEventListAccess(newUser(), clientID)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.Contains(t, err.Error(), "not enabled for this application")
	err = RequireEventClientForCreate(newUser(), &clientID)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.Contains(t, err.Error(), "not enabled for this application")

	// Entitled but outside the session application: the reach still denies.
	reachable, reachErr, entitled = false, nil, true
	err = RequireEventListAccess(newUser(), clientID)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.Contains(t, err.Error(), "outside the current application")
	err = RequireEventClientForCreate(newUser(), &clientID)
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	assert.Contains(t, err.Error(), "outside the current application")
	err = RequireClientCapability(newUser(), clientID, CapabilityMembersManage)
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	// A reach lookup error is a 500, never a fall-through or a 403.
	reachable, reachErr, entitled = false, errors.New("connection reset"), false
	err = RequireEventListAccess(newUser(), clientID)
	assert.Equal(t, http.StatusInternalServerError, statusOf(err))
	assert.NotContains(t, err.Error(), "connection reset")
	err = RequireClientCapability(newUser(), clientID, CapabilityMembersManage)
	assert.Equal(t, http.StatusInternalServerError, statusOf(err))
	err = RequireClientAccess(newUser(), clientID)
	assert.Equal(t, http.StatusInternalServerError, statusOf(err))
}
