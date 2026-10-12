package authz

import (
	"errors"
	"net/http"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func moveAccessUser(t *testing.T, roles map[uuid.UUID]string) *models.User {
	t.Helper()
	restore := ReplaceHooksForTest(Hooks{
		CheckAccessRecursive: func(userID, clientID uuid.UUID) (bool, string) {
			role, ok := roles[clientID]
			return ok, role
		},
	})
	t.Cleanup(restore)
	return &models.User{ID: uuid.Must(uuid.NewV4())}
}

func failureStatus(t *testing.T, err error) int {
	t.Helper()
	var failure *Failure
	require.True(t, errors.As(err, &failure), "expected *Failure, got %v", err)
	return failure.Status
}

// EVT-4: client_id null on update used to pass and orphan the event.
func TestRequireClientMoveAccessRejectsDetachingAnEvent(t *testing.T) {
	orgA := uuid.Must(uuid.NewV4())
	user := moveAccessUser(t, map[uuid.UUID]string{orgA: "OWNER"})

	err := RequireClientMoveAccess(user, &orgA, nil)
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, failureStatus(t, err))

	assert.NoError(t, RequireClientMoveAccess(user, nil, nil), "a legacy unlinked event may stay unlinked")
	assert.NoError(t, RequireClientMoveAccess(user, &orgA, &orgA), "unchanged")
}

func TestRequireClientMoveAccessNeedsManageInBothOrganizations(t *testing.T) {
	orgA := uuid.Must(uuid.NewV4())
	orgB := uuid.Must(uuid.NewV4())

	onlyTarget := moveAccessUser(t, map[uuid.UUID]string{orgB: "OWNER"})
	err := RequireClientMoveAccess(onlyTarget, &orgA, &orgB)
	require.Error(t, err, "event-level access to the source is not enough to move the event out")
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err))

	onlySource := moveAccessUser(t, map[uuid.UUID]string{orgA: "OWNER"})
	err = RequireClientMoveAccess(onlySource, &orgA, &orgB)
	require.Error(t, err)
	assert.Equal(t, http.StatusForbidden, failureStatus(t, err))

	both := moveAccessUser(t, map[uuid.UUID]string{orgA: "OWNER", orgB: "OWNER"})
	assert.NoError(t, RequireClientMoveAccess(both, &orgA, &orgB))
}
