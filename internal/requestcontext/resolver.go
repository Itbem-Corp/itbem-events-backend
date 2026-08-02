package requestcontext

import (
	"errors"
	"strings"

	"github.com/gofrs/uuid"
)

type Input struct {
	AuthenticatedApplication string
	RequestedApplication     string
	SessionApplication       string
	RequestedWorkspaceMode   string
	RequestedOrganizationID  string
	AllowsPlatformAdmin      bool
	IsRoot                   bool
	OrganizationAllowed      func(uuid.UUID) bool
}

type Resolved struct {
	WorkspaceMode   string
	OrganizationID  uuid.UUID
	HasOrganization bool
}

func Resolve(input Input) (Resolved, error) {
	authenticatedApplication := normalize(input.AuthenticatedApplication)
	requestedApplication := normalize(input.RequestedApplication)
	if requestedApplication != "" && requestedApplication != authenticatedApplication {
		return Resolved{}, errors.New("application header does not match the authenticated tenant")
	}
	if normalize(input.SessionApplication) != authenticatedApplication {
		return Resolved{}, errors.New("application session does not match the authenticated tenant")
	}

	workspaceMode := normalize(input.RequestedWorkspaceMode)
	if workspaceMode == "" {
		workspaceMode = WorkspaceOrganization
	}
	if workspaceMode != WorkspaceOrganization && workspaceMode != WorkspacePlatform {
		return Resolved{}, errors.New("workspace mode must be organization or platform")
	}

	organizationValue := strings.TrimSpace(input.RequestedOrganizationID)
	if workspaceMode == WorkspacePlatform {
		if organizationValue != "" {
			return Resolved{}, errors.New("platform workspace cannot include an organization")
		}
		if !input.AllowsPlatformAdmin || !input.IsRoot {
			return Resolved{}, errors.New("platform workspace is not enabled for this session")
		}
		return Resolved{WorkspaceMode: WorkspacePlatform}, nil
	}

	resolved := Resolved{WorkspaceMode: WorkspaceOrganization}
	if organizationValue == "" {
		return resolved, nil
	}
	organizationID, err := uuid.FromString(organizationValue)
	if err != nil {
		return Resolved{}, errors.New("organization header must be a valid UUID")
	}
	if !input.AllowsPlatformAdmin || !input.IsRoot {
		if input.OrganizationAllowed == nil || !input.OrganizationAllowed(organizationID) {
			return Resolved{}, errors.New("organization is not enabled for this application session")
		}
	}
	resolved.OrganizationID = organizationID
	resolved.HasOrganization = true
	return resolved, nil
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
