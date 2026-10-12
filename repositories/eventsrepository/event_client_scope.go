package eventsrepository

import (
	"sync/atomic"

	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

// eventClientScopeEnabled mirrors EVENT_CLIENT_ACCESS. While it is off the
// user-scoped event lists are byte-for-byte today's queries.
var eventClientScopeEnabled atomic.Bool

// ConfigureEventClientScope turns grant visibility in user-scoped event lists
// on or off. It is called once at startup.
func ConfigureEventClientScope(enabled bool) {
	eventClientScopeEnabled.Store(enabled)
}

// eventClientGrantVisibleSQL selects events the user reaches through an
// active event-client grant. It applies the same predicates as authorization
// (eventclientrepository.HasActiveGrant): not revoked, the event still in the
// grant's organization, the organization active, and no client_members row
// of the holder in that organization. The product boundary is not checked
// here; every action on a listed row still goes through authz.
const eventClientGrantVisibleSQL = `EXISTS (
	SELECT 1 FROM event_client_grants g
	JOIN clients gc ON gc.id = g.organization_id AND gc.deleted_at IS NULL AND gc.is_active = true
	WHERE g.event_id = events.id
		AND g.organization_id = events.client_id
		AND g.user_id = ?
		AND g.revoked_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM client_members cm2 WHERE cm2.user_id = g.user_id AND cm2.client_id = g.organization_id)
)`

// withEventClientGrants applies the caller's organization visibility
// predicate and, when event-client access is enabled, ORs in the events the
// caller holds a grant on. Callers pass their own organization predicate so
// organization-side narrowing composes without touching the grant rule.
func withEventClientGrants(query *gorm.DB, userID uuid.UUID, organizationSQL string, organizationArgs ...interface{}) *gorm.DB {
	if !eventClientScopeEnabled.Load() {
		return query.Where(organizationSQL, organizationArgs...)
	}
	args := append(append([]interface{}{}, organizationArgs...), userID)
	return query.Where("(("+organizationSQL+") OR "+eventClientGrantVisibleSQL+")", args...)
}
