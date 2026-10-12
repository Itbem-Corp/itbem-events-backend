package events

import "github.com/gofrs/uuid"

// InvitationLiveGuestChecker is implemented by the invitation repository. It is
// an optional extension of ports.InvitationRepository (a core port), detected
// with a type assertion.
type InvitationLiveGuestChecker interface {
	InvitationHasLiveGuest(invitationID uuid.UUID) (bool, error)
}

// InvitationLiveGuestLookup returns the live-guest check of repo, or nil when
// repo does not implement it (test doubles).
func InvitationLiveGuestLookup(repo any) func(uuid.UUID) (bool, error) {
	if checker, ok := repo.(InvitationLiveGuestChecker); ok && checker != nil {
		return checker.InvitationHasLiveGuest
	}
	return nil
}

// InvitationHeldByLiveGuest is the public token rule: an invitation grants
// access only while a non-deleted guest holds it. A lookup error denies.
func InvitationHeldByLiveGuest(lookup func(uuid.UUID) (bool, error), invitationID uuid.UUID) bool {
	if lookup == nil {
		return true
	}
	live, err := lookup(invitationID)
	return err == nil && live
}
