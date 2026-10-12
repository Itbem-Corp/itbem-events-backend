package applications

import (
	"strings"

	"github.com/gofrs/uuid"
)

// AllowsOrganization is the shared authorization boundary for selecting an
// organization from an authenticated application session.
func (session *Session) AllowsOrganization(organizationID uuid.UUID) bool {
	if session == nil || organizationID == uuid.Nil {
		return false
	}
	for index := range session.Organizations {
		if session.Organizations[index].ID == organizationID {
			return true
		}
	}
	return false
}

// HasAnyCapability keeps application-surface checks consistent across
// middleware and controllers without exposing capability matching details.
func (session *Session) HasAnyCapability(expected ...string) bool {
	if session == nil || len(expected) == 0 {
		return false
	}
	for _, actual := range session.Capabilities {
		for _, candidate := range expected {
			if actual == candidate {
				return true
			}
		}
	}
	return false
}

// ApplicationCode is the lower-case code of the authenticated application.
func (session *Session) ApplicationCode() string {
	if session == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(session.Application.Code))
}

// PolicyCeiling returns the session names an active user_application_policy
// allows, and whether such a policy exists. internal/authz uses it as the
// server-side ceiling (AUTHZ_POLICY_CEILING). The map must not be modified.
func (session *Session) PolicyCeiling() (map[string]struct{}, bool) {
	if session == nil || !session.policyRestricted {
		return nil, false
	}
	return session.policyCapabilities, true
}
