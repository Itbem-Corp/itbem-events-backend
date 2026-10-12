package dtos

import (
	"testing"

	"events-stocks/models"

	"github.com/stretchr/testify/assert"
)

// ROLES-97: user/caller-scoped organization lists mark inherited access.
func TestNewClientSummaryResponse_MarksInheritedAccess(t *testing.T) {
	assert.True(t, NewClientSummaryResponse(models.Client{AccessRole: "INHERITED_OWNER"}).Inherited)
	assert.True(t, NewClientSummaryResponse(models.Client{AccessRole: "inherited_admin"}).Inherited)
	assert.False(t, NewClientSummaryResponse(models.Client{AccessRole: "Owner"}).Inherited)
	assert.False(t, NewClientSummaryResponse(models.Client{}).Inherited)
}
