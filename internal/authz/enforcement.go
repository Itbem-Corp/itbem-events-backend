package authz

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"events-stocks/internal/capabilities"
	"events-stocks/internal/products"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// ApplicationSessionContextKey is where middleware/applicationaccess stores
// the resolved application session (its ContextKey). A test in an external
// package pins the two constants together.
const ApplicationSessionContextKey = "application_session"

const requestMemoContextKey = "authz_request_memo"

// PolicyCeilingSource is the part of the application session that authz
// reads. services/applications.Session implements it; authz cannot import
// that package (it would cycle through productmetricshttp).
type PolicyCeilingSource interface {
	ApplicationCode() string
	// PolicyCeiling returns the session names an active
	// user_application_policy allows, and whether such a policy exists.
	PolicyCeiling() (map[string]struct{}, bool)
}

// ApplicationAccess is the organization lookup plus the product entitlement
// of one (user, organization) pair.
type ApplicationAccess struct {
	// Allowed and Role follow CheckAccessRecursive: a direct membership, or an
	// inherited Owner/Admin membership ("INHERITED_" prefix).
	Allowed bool
	Role    string
	// Entitled is true when the target organization is active, its nearest
	// branded ancestor is the requested product, that product is enabled on
	// the branded root and the membership has an active entitlement to it.
	Entitled bool
	// Reason names the first failed entitlement condition:
	// organization_inactive, product_mismatch, product_disabled or
	// member_not_entitled.
	Reason string
}

type accessScope int

const (
	// scopeOrganization is organization, member and application management.
	// Product entitlement never applies, so an Owner can repair access.
	scopeOrganization accessScope = iota
	// scopeEventResource is every event, guest, moment, section, invitation
	// and resource check plus event lists and event creation.
	scopeEventResource
)

var enforcement = struct {
	mu          sync.RWMutex
	entitlement capabilities.Mode
	ceiling     capabilities.Mode
}{entitlement: capabilities.ModeOff, ceiling: capabilities.ModeOff}

// ConfigureEnforcement sets AUTHZ_PRODUCT_ENTITLEMENT and
// AUTHZ_POLICY_CEILING. It is called once at startup; it returns a function
// that restores the previous modes (for tests).
func ConfigureEnforcement(entitlement, ceiling capabilities.Mode) func() {
	enforcement.mu.Lock()
	previousEntitlement, previousCeiling := enforcement.entitlement, enforcement.ceiling
	enforcement.entitlement, enforcement.ceiling = normalizeMode(entitlement), normalizeMode(ceiling)
	enforcement.mu.Unlock()
	return func() {
		enforcement.mu.Lock()
		enforcement.entitlement, enforcement.ceiling = previousEntitlement, previousCeiling
		enforcement.mu.Unlock()
	}
}

// EntitlementMode reports the product entitlement rollout mode.
func EntitlementMode() capabilities.Mode {
	enforcement.mu.RLock()
	defer enforcement.mu.RUnlock()
	return enforcement.entitlement
}

// PolicyCeilingMode reports the application policy ceiling rollout mode.
func PolicyCeilingMode() capabilities.Mode {
	enforcement.mu.RLock()
	defer enforcement.mu.RUnlock()
	return enforcement.ceiling
}

func normalizeMode(mode capabilities.Mode) capabilities.Mode {
	switch mode {
	case capabilities.ModeShadow, capabilities.ModeEnforce:
		return mode
	default:
		return capabilities.ModeOff
	}
}

// requestMemo memoizes organization lookups for one HTTP request, so an
// endpoint that checks several capabilities on one event (for example
// GET /api/events/:id/capabilities) runs one access query. It lives in the
// echo context and is shared by every per-request user copy.
type requestMemo struct {
	mu      sync.Mutex
	route   string
	tenant  string
	entries map[requestMemoKey]ApplicationAccess
}

type requestMemoKey struct {
	userID      uuid.UUID
	clientID    uuid.UUID
	entitlement bool
}

// stampRequestAuthorization copies the policy ceiling of the application
// session onto the per-request user and attaches the request memo.
func stampRequestAuthorization(c echo.Context, user *models.User) {
	if c == nil || user == nil {
		return
	}
	tenant, _ := c.Get("tenant_code").(string)
	tenant = strings.ToLower(strings.TrimSpace(tenant))
	user.AuthApplicationCode = tenant
	user.AuthPolicyRestricted = false
	user.AuthPolicyCapabilities = nil
	if source, ok := c.Get(ApplicationSessionContextKey).(PolicyCeilingSource); ok && source != nil {
		if code := strings.ToLower(strings.TrimSpace(source.ApplicationCode())); code != "" {
			user.AuthApplicationCode = code
		}
		user.AuthPolicyCapabilities, user.AuthPolicyRestricted = source.PolicyCeiling()
	}
	memo, _ := c.Get(requestMemoContextKey).(*requestMemo)
	if memo == nil {
		memo = &requestMemo{route: c.Path(), tenant: tenant, entries: make(map[requestMemoKey]ApplicationAccess)}
		c.Set(requestMemoContextKey, memo)
	}
	user.AuthRequestMemo = memo
}

func memoOf(user *models.User) *requestMemo {
	if user == nil {
		return nil
	}
	memo, _ := user.AuthRequestMemo.(*requestMemo)
	return memo
}

// lookupOrganizationAccess resolves the caller's organization role for
// clientID. For event resources with AUTHZ_PRODUCT_ENTITLEMENT shadow or
// enforce, the same single query also answers the entitlement. Lookup errors
// are 500s: they never fall through to a weaker authority path.
func lookupOrganizationAccess(user *models.User, clientID uuid.UUID, scope accessScope) (ApplicationAccess, error) {
	mode := EntitlementMode()
	evaluateEntitlement := scope == scopeEventResource && mode != capabilities.ModeOff
	key := requestMemoKey{userID: user.ID, clientID: clientID, entitlement: evaluateEntitlement}
	memo := memoOf(user)
	if memo != nil {
		memo.mu.Lock()
		cached, ok := memo.entries[key]
		memo.mu.Unlock()
		if ok {
			return cached, nil
		}
	}

	var access ApplicationAccess
	switch {
	case evaluateEntitlement && hooks.CheckApplicationAccessRecursive != nil:
		resolved, err := hooks.CheckApplicationAccessRecursive(user.ID, clientID, products.DefaultCode.String())
		if err != nil {
			return ApplicationAccess{}, organizationLookupFailure(err)
		}
		access = resolved
	case evaluateEntitlement && mode == capabilities.ModeEnforce:
		return ApplicationAccess{}, dependencyFailure("CheckApplicationAccessRecursive")
	default:
		allowed, role, err := checkAccessRecursive(user.ID, clientID)
		if err != nil {
			return ApplicationAccess{}, err
		}
		access = ApplicationAccess{Allowed: allowed, Role: role, Entitled: true}
	}

	if memo != nil {
		memo.mu.Lock()
		memo.entries[key] = access
		memo.mu.Unlock()
	}
	return access, nil
}

func checkAccessRecursive(userID, clientID uuid.UUID) (bool, string, error) {
	if hooks.CheckAccessRecursiveE != nil {
		allowed, role, err := hooks.CheckAccessRecursiveE(userID, clientID)
		if err != nil {
			return false, "", organizationLookupFailure(err)
		}
		return allowed, role, nil
	}
	if hooks.CheckAccessRecursive != nil {
		allowed, role := hooks.CheckAccessRecursive(userID, clientID)
		return allowed, role, nil
	}
	return false, "", dependencyFailure("CheckAccessRecursive")
}

func organizationLookupFailure(err error) *Failure {
	slog.Error("authz organization lookup failed", "error", err)
	return &Failure{Status: http.StatusInternalServerError, Message: "Authorization error", Detail: "Organization access lookup failed"}
}

// finalizeAuthorityDecision is the single hook every allowed authority path
// passes through before returning: product entitlement, then the
// application policy ceiling. The event-client grant path (EVENT_CLIENT, P3)
// calls it with ApplicationAccess{Allowed: true, Entitled: <event org
// resolves to eventiapp>, Reason: "product_mismatch" when not} so grants get
// the same shadow/enforce behaviour as memberships.
func finalizeAuthorityDecision(user *models.User, clientID uuid.UUID, capability Capability, access ApplicationAccess) error {
	if err := enforceProductEntitlement(user, clientID, capability, access); err != nil {
		return err
	}
	return enforcePolicyCeiling(user, capability)
}

// enforceProductEntitlement applies AUTHZ_PRODUCT_ENTITLEMENT to an allowed
// organization decision. off never reaches here with Entitled=false.
func enforceProductEntitlement(user *models.User, clientID uuid.UUID, capability Capability, access ApplicationAccess) error {
	if !access.Allowed || access.Entitled {
		return nil
	}
	reason := access.Reason
	if reason == "" {
		reason = "member_not_entitled"
	}
	switch EntitlementMode() {
	case capabilities.ModeShadow:
		reportShadowDenial(user, "entitlement", clientID, capability, reason)
		return nil
	case capabilities.ModeEnforce:
		return &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your organization is not enabled for this application"}
	default:
		return nil
	}
}

// enforcePolicyCeiling applies AUTHZ_POLICY_CEILING: an active
// user_application_policy caps the allow-listed capabilities it governs.
// Names outside the allow-list (events:delete, events:clients:manage, ...)
// and users without a policy are never affected. The primary root is exempt.
func enforcePolicyCeiling(user *models.User, capability Capability) error {
	mode := PolicyCeilingMode()
	if mode == capabilities.ModeOff || user == nil || !user.AuthPolicyRestricted || user.IsPrimaryRoot() {
		return nil
	}
	name, ok := capabilities.SessionName(capability)
	if !ok || !capabilities.PolicyAllows(user.AuthApplicationCode, name) {
		return nil
	}
	if _, allowed := user.AuthPolicyCapabilities[name]; allowed {
		return nil
	}
	if mode == capabilities.ModeShadow {
		reportShadowDenial(user, "policy", uuid.Nil, capability, "policy_excludes")
		return nil
	}
	return &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your application access policy does not allow this action"}
}

const (
	shadowDedupeTTL     = 10 * time.Minute
	shadowDedupeEntries = 10000
)

var shadowLog = newShadowDeduper(shadowDedupeTTL, shadowDedupeEntries, time.Now)

// reportShadowDenial logs a would-deny once per (user, organization,
// capability, check) every 10 minutes. The log carries no user identifiers;
// counts come from the logs (there is no metrics library).
func reportShadowDenial(user *models.User, check string, clientID uuid.UUID, capability Capability, reason string) {
	var userID uuid.UUID
	tenant, route := "", ""
	if user != nil {
		userID = user.ID
		tenant = user.AuthApplicationCode
	}
	if memo := memoOf(user); memo != nil {
		route = memo.route
		if tenant == "" {
			tenant = memo.tenant
		}
	}
	key := check + "|" + userID.String() + "|" + clientID.String() + "|" + string(capability)
	if !shadowLog.first(key) {
		return
	}
	slog.Warn("authz shadow denial",
		"check", check,
		"tenant", tenant,
		"capability", string(capability),
		"reason", reason,
		"route", route,
	)
}

type shadowDeduper struct {
	mu   sync.Mutex
	ttl  time.Duration
	max  int
	now  func() time.Time
	seen map[string]time.Time
}

func newShadowDeduper(ttl time.Duration, max int, now func() time.Time) *shadowDeduper {
	return &shadowDeduper{ttl: ttl, max: max, now: now, seen: make(map[string]time.Time)}
}

// first reports whether key has not been seen within the TTL, and records it.
// The map is bounded: expired keys are pruned when it is full, and if it is
// still full it is reset (a few duplicate logs are better than unbounded
// memory).
func (d *shadowDeduper) first(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if seenAt, ok := d.seen[key]; ok && now.Sub(seenAt) < d.ttl {
		return false
	}
	if len(d.seen) >= d.max {
		for candidate, seenAt := range d.seen {
			if now.Sub(seenAt) >= d.ttl {
				delete(d.seen, candidate)
			}
		}
		if len(d.seen) >= d.max {
			d.seen = make(map[string]time.Time)
		}
	}
	d.seen[key] = now
	return true
}
