package applications

import (
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
)

func TestSessionAuthorizationHelpers(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	session := &Session{
		Organizations: []OrganizationAccess{{ID: organizationID}},
		Capabilities:  []string{"events:view", "members:manage"},
	}

	assert.True(t, session.AllowsOrganization(organizationID))
	assert.False(t, session.AllowsOrganization(uuid.Must(uuid.NewV4())))
	assert.False(t, session.AllowsOrganization(uuid.Nil))
	assert.True(t, session.HasAnyCapability("organizations:manage", "members:manage"))
	assert.False(t, session.HasAnyCapability("organizations:manage"))
	assert.False(t, session.HasAnyCapability())
}

func TestNilSessionAuthorizationHelpersFailClosed(t *testing.T) {
	var session *Session
	assert.False(t, session.AllowsOrganization(uuid.Must(uuid.NewV4())))
	assert.False(t, session.HasAnyCapability("events:view"))
}
