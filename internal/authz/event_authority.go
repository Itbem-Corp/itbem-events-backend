package authz

import (
	"net/http"
	"strings"

	"events-stocks/internal/products"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

type authorityKind int

const (
	authorityOrganization authorityKind = iota
	authorityPlatform
	authorityEventClient
)

// eventAuthority records which precedence level authorized a caller on one
// event: P1 platform root, P2 organization membership, P3 event-client grant.
type eventAuthority struct {
	kind authorityKind
	// access is the organization lookup (role plus product entitlement) for
	// authorityOrganization; it is applied per capability through
	// finalizeAuthorityDecision.
	access ApplicationAccess
}

// eventClientGrantAccess is the ApplicationAccess a grant passes to
// finalizeAuthorityDecision. eventClientGrantApplies already requires the
// event organization to resolve to EventiApp (a hard gate, not flagged), so
// the grant is entitled; the policy ceiling still applies.
func eventClientGrantAccess() ApplicationAccess {
	return ApplicationAccess{Allowed: true, Entitled: true}
}

// authorizeEventView applies AUTHZ_PRODUCT_ENTITLEMENT to a plain event read
// (RequireEventAccess / EnsureEventAccess) by an organization member, the
// same rule as RequireEventListAccess. Roots and grants are unaffected.
func authorizeEventView(user *models.User, event *models.Event, authority eventAuthority) error {
	if authority.kind != authorityOrganization || event == nil || event.ClientID == nil {
		return nil
	}
	return enforceProductEntitlement(user, *event.ClientID, CapabilityView, authority.access)
}

const eventAuthorityContextPrefix = "authz.event_authority:"

// eventClientCeiling is the complete set of event capabilities an external
// event client (event_client_grants) may ever hold: the event-level powers of
// an EVENT_MANAGER, without create, delete, members, organization, client
// management or organization metrics.
var eventClientCeiling = []Capability{
	CapabilityView,
	CapabilityEventManage,
	CapabilityGuestManage,
	CapabilityCheckin,
	CapabilityAnalyticsView,
}

// EventClientCapabilityCeiling returns a copy of the event-client ceiling so
// the session projection can be checked against it without an import cycle.
func EventClientCapabilityCeiling() []Capability {
	return append([]Capability(nil), eventClientCeiling...)
}

func eventClientHasCapability(capability Capability) bool {
	for _, allowed := range eventClientCeiling {
		if allowed == capability {
			return true
		}
	}
	return false
}

func eventAuthorityContextKey(event *models.Event) string {
	clientID := "none"
	if event.ClientID != nil {
		clientID = event.ClientID.String()
	}
	return eventAuthorityContextPrefix + event.ID.String() + ":" + clientID
}

// memoizedEventAuthority resolves the authority once per request and event,
// so an endpoint that evaluates several capabilities performs one
// organization lookup (and at most one grant lookup).
func memoizedEventAuthority(c echo.Context, user *models.User, event *models.Event) (eventAuthority, error) {
	if c == nil {
		return resolveEventAuthority(user, event)
	}
	key := eventAuthorityContextKey(event)
	if cached, ok := c.Get(key).(eventAuthority); ok {
		return cached, nil
	}
	authority, err := resolveEventAuthority(user, event)
	if err != nil {
		return eventAuthority{}, err
	}
	c.Set(key, authority)
	return authority, nil
}

// resolveEventAuthority applies the fixed precedence root > organization >
// event-client grant > deny. Every lookup error fails closed with a 500; an
// organization lookup error never falls through to the grant path.
func resolveEventAuthority(user *models.User, event *models.Event) (eventAuthority, error) {
	if user == nil || event == nil {
		return eventAuthority{}, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "You do not have access to this client"}
	}
	if user.IsPlatformAdmin() {
		return eventAuthority{kind: authorityPlatform}, nil
	}
	if event.ClientID == nil {
		return eventAuthority{}, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Event is not linked to a client"}
	}
	clientID := *event.ClientID
	if err := requireTenantClientBoundary(user, clientID); err != nil {
		return eventAuthority{}, err
	}
	access, err := lookupOrganizationAccess(user, clientID, scopeEventResource)
	if err != nil {
		return eventAuthority{}, err
	}
	if access.Allowed {
		return eventAuthority{kind: authorityOrganization, access: access}, nil
	}
	granted, err := eventClientGrantApplies(user, event.ID, clientID)
	if err != nil {
		return eventAuthority{}, err
	}
	if granted {
		return eventAuthority{kind: authorityEventClient}, nil
	}
	return eventAuthority{}, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "You do not have access to this client"}
}

// eventClientGrantApplies is P3: the flag hook is configured, the caller did
// not sign in through a branded tenant, an active grant exists (the lookup
// already excludes current and former organization members) and the event's
// organization belongs to EventiApp by the nearest-branded-ancestor rule.
func eventClientGrantApplies(user *models.User, eventID, clientID uuid.UUID) (bool, error) {
	if hooks.HasEventClientGrant == nil || strings.TrimSpace(user.AuthTenantCode) != "" {
		return false, nil
	}
	granted, err := hooks.HasEventClientGrant(eventID, user.ID)
	if err != nil {
		return false, &Failure{Status: http.StatusInternalServerError, Message: "Authorization error", Detail: "event client grant lookup failed"}
	}
	if !granted {
		return false, nil
	}
	if hooks.ResolveClientProduct == nil {
		return false, dependencyFailure("ResolveClientProduct")
	}
	product, err := hooks.ResolveClientProduct(clientID)
	if err != nil {
		return false, &Failure{Status: http.StatusInternalServerError, Message: "Authorization error", Detail: "organization product lookup failed"}
	}
	return products.SupportsEventOperations(product), nil
}

// EventCapabilityForOrganizationEvents evaluates one capability for many
// events of the same organization with one organization lookup and one
// event-member batch. It never honours event-client grants: its callers are
// organization managers acting on their organization's events.
func EventCapabilityForOrganizationEvents(user *models.User, orgID uuid.UUID, eventIDs []uuid.UUID, capability Capability) (map[uuid.UUID]bool, error) {
	result := make(map[uuid.UUID]bool, len(eventIDs))
	if user == nil {
		return result, nil
	}
	if err := requireTenantClientBoundary(user, orgID); err != nil {
		return result, nil
	}
	if user.IsPlatformAdmin() {
		allowed := platformHasCapability(user, capability)
		if allowed {
			ceilingAllowed, err := finalAuthorityAllows(enforcePolicyCeiling(user, capability))
			if err != nil {
				return nil, err
			}
			allowed = ceilingAllowed
		}
		for _, id := range eventIDs {
			result[id] = allowed
		}
		return result, nil
	}
	access, err := lookupOrganizationAccess(user, orgID, scopeEventResource)
	if err != nil {
		return nil, err
	}
	allowed := access.Allowed && roleHasCapability(access.Role, capability)
	if allowed {
		allowed, err = finalAuthorityAllows(finalizeAuthorityDecision(user, orgID, capability, access))
		if err != nil {
			return nil, err
		}
	}
	if !allowed {
		for _, id := range eventIDs {
			result[id] = false
		}
		return result, nil
	}
	roles := map[uuid.UUID]string{}
	if len(eventIDs) > 0 {
		if hooks.GetEventMemberRoles == nil {
			return nil, dependencyFailure("GetEventMemberRoles")
		}
		roles, err = hooks.GetEventMemberRoles(eventIDs, user.ID)
		if err != nil {
			return nil, &Failure{Status: http.StatusInternalServerError, Message: "Authorization error", Detail: "event member lookup failed"}
		}
	}
	for _, id := range eventIDs {
		role, found := roles[id]
		result[id] = !found || eventRoleHasCapability(role, capability)
	}
	return result, nil
}

// finalAuthorityAllows turns a finalizeAuthorityDecision result into an
// allow flag for batch evaluation: a deny (4xx) is false, a dependency or
// lookup failure (5xx or an untyped error) is returned so it is never read as
// a silent deny.
func finalAuthorityAllows(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if failure, ok := err.(*Failure); ok && failure.Status < http.StatusInternalServerError {
		return false, nil
	}
	return false, err
}
