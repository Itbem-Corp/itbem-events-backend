package authz_test

import (
	"testing"

	"events-stocks/internal/authz"
	"events-stocks/middleware/applicationaccess"
	"events-stocks/services/applications"

	"github.com/stretchr/testify/assert"
)

// authz reads the policy ceiling from the session that applicationaccess
// stores. They cannot import each other, so this external test pins the
// shared context key and the interface.
func TestApplicationSessionKeyMatchesApplicationAccessMiddleware(t *testing.T) {
	assert.Equal(t, applicationaccess.ContextKey, authz.ApplicationSessionContextKey)
}

func TestApplicationSessionImplementsPolicyCeilingSource(t *testing.T) {
	var source authz.PolicyCeilingSource = &applications.Session{}
	_, restricted := source.PolicyCeiling()
	assert.False(t, restricted)
}
