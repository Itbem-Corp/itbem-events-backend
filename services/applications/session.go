package applications

import "github.com/gofrs/uuid"

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
