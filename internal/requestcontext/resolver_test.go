package requestcontext

import (
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRequestContext(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	base := Input{
		AuthenticatedApplication: "eventiapp",
		SessionApplication:       "eventiapp",
		OrganizationAllowed:      func(candidate uuid.UUID) bool { return candidate == organizationID },
	}

	resolved, err := Resolve(base)
	require.NoError(t, err)
	assert.Equal(t, WorkspaceOrganization, resolved.WorkspaceMode)
	assert.False(t, resolved.HasOrganization)

	organization := base
	organization.RequestedOrganizationID = organizationID.String()
	resolved, err = Resolve(organization)
	require.NoError(t, err)
	assert.Equal(t, organizationID, resolved.OrganizationID)
	assert.True(t, resolved.HasOrganization)

	platform := base
	platform.RequestedWorkspaceMode = WorkspacePlatform
	platform.AllowsPlatformAdmin = true
	platform.IsRoot = true
	resolved, err = Resolve(platform)
	require.NoError(t, err)
	assert.Equal(t, WorkspacePlatform, resolved.WorkspaceMode)
}

func TestResolveRejectsCrossBoundaryContext(t *testing.T) {
	organizationID := uuid.Must(uuid.NewV4())
	tests := []Input{
		{AuthenticatedApplication: "eventiapp", RequestedApplication: "itbem", SessionApplication: "eventiapp"},
		{AuthenticatedApplication: "eventiapp", SessionApplication: "itbem"},
		{AuthenticatedApplication: "eventiapp", SessionApplication: "eventiapp", RequestedWorkspaceMode: "unknown"},
		{AuthenticatedApplication: "eventiapp", SessionApplication: "eventiapp", RequestedWorkspaceMode: WorkspacePlatform, RequestedOrganizationID: organizationID.String(), AllowsPlatformAdmin: true, IsRoot: true},
		{AuthenticatedApplication: "eventiapp", SessionApplication: "eventiapp", RequestedWorkspaceMode: WorkspacePlatform},
		{AuthenticatedApplication: "eventiapp", SessionApplication: "eventiapp", RequestedOrganizationID: organizationID.String()},
	}
	for _, input := range tests {
		_, err := Resolve(input)
		require.Error(t, err)
	}
}
