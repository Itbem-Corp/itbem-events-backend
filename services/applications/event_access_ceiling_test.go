package applications

import (
	"testing"

	"events-stocks/internal/capabilities"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resolveGrantOnlyWithPolicy(t *testing.T, mode capabilities.Mode) *Session {
	t.Helper()
	db, mock := applicationServiceTestDB(t)
	userID := uuid.Must(uuid.NewV4())
	appID := expectApplicationWithModules(mock, "eventiapp", true, eventiAppModules)
	expectOrganizations(mock, appID, userID, emptyOrganizationRows())
	expectUserApplicationPolicy(mock, userID, appID, `["events:view","events:manage"]`)
	service := NewSessionService(db, func(string) (*models.User, error) {
		return &models.User{ID: userID, IsActive: true}, nil
	})
	service.ConfigureEventClientAccess(func(uuid.UUID) (int, error) { return 1, nil })
	service.ConfigurePolicyCeiling(mode)
	session, err := service.Resolve("client-sub", "eventiapp")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.NotNil(t, session.EventAccess)
	return session
}

// AUTHZ_POLICY_CEILING=enforce caps event_access.capabilities the same way it
// caps the per-organization lists; off and shadow leave them unchanged.
func TestEventAccessCapabilitiesAreCappedByThePolicyOnlyInEnforce(t *testing.T) {
	for _, mode := range []capabilities.Mode{capabilities.ModeOff, capabilities.ModeShadow} {
		session := resolveGrantOnlyWithPolicy(t, mode)
		assert.Contains(t, session.EventAccess.Capabilities, "guests:manage", mode)
	}
	session := resolveGrantOnlyWithPolicy(t, capabilities.ModeEnforce)
	assert.Equal(t, []string{"events:manage", "events:view"}, session.EventAccess.Capabilities)
	assert.NotContains(t, session.Capabilities, "guests:manage")
}
