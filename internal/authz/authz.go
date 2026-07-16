package authz

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"events-stocks/models"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type Hooks struct {
	SyncUser              func(cognitoSub string) (*models.User, error)
	CheckAccessRecursive  func(userID, targetClientID uuid.UUID) (bool, string)
	GetClientByID         func(clientID uuid.UUID) (*models.Client, error)
	GetEventByIDRaw       func(eventID uuid.UUID) (*models.Event, error)
	GetEventSectionByID   func(sectionID uuid.UUID) (*models.EventSection, error)
	GetMomentByID         func(momentID uuid.UUID) (*models.Moment, error)
	GetGuestByID          func(guestID uuid.UUID) (*models.Guest, error)
	GetInvitationByIDLite func(invitationID uuid.UUID) (*models.Invitation, error)
	GetResourceByID       func(resourceID uuid.UUID) (*models.Resource, error)
	GetEventMemberRole    func(eventID, userID uuid.UUID) (role string, found bool, err error)
}

var hooks Hooks

func Configure(configured Hooks) {
	hooks = configured
}

func ReplaceHooksForTest(replacement Hooks) func() {
	previous := hooks
	if replacement.SyncUser != nil {
		hooks.SyncUser = replacement.SyncUser
	}
	if replacement.CheckAccessRecursive != nil {
		hooks.CheckAccessRecursive = replacement.CheckAccessRecursive
	}
	if replacement.GetClientByID != nil {
		hooks.GetClientByID = replacement.GetClientByID
	}
	if replacement.GetEventByIDRaw != nil {
		hooks.GetEventByIDRaw = replacement.GetEventByIDRaw
	}
	if replacement.GetEventSectionByID != nil {
		hooks.GetEventSectionByID = replacement.GetEventSectionByID
	}
	if replacement.GetMomentByID != nil {
		hooks.GetMomentByID = replacement.GetMomentByID
	}
	if replacement.GetGuestByID != nil {
		hooks.GetGuestByID = replacement.GetGuestByID
	}
	if replacement.GetInvitationByIDLite != nil {
		hooks.GetInvitationByIDLite = replacement.GetInvitationByIDLite
	}
	if replacement.GetResourceByID != nil {
		hooks.GetResourceByID = replacement.GetResourceByID
	}
	if replacement.GetEventMemberRole != nil {
		hooks.GetEventMemberRole = replacement.GetEventMemberRole
	}
	return func() {
		hooks = previous
	}
}

type Failure struct {
	Status  int
	Message string
	Detail  string
}

// Capability is an action, not a screen. Controllers use it to keep server
// enforcement independent from what the dashboard chooses to render.
type Capability string

const (
	CapabilityView          Capability = "view"
	CapabilityEventManage   Capability = "event:manage"
	CapabilityGuestManage   Capability = "guest:manage"
	CapabilityCheckin       Capability = "checkin:run"
	CapabilityAnalyticsView Capability = "analytics:view"
	CapabilityMembersManage Capability = "members:manage"
	CapabilityOrgManage     Capability = "organization:manage"
)

func (e *Failure) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Message
}

func Respond(c echo.Context, err error) error {
	if failure, ok := err.(*Failure); ok {
		return utils.Error(c, failure.Status, failure.Message, failure.Detail)
	}
	return utils.Error(c, http.StatusInternalServerError, "Authorization error", err.Error())
}

func lookupFailure(kind string, err error) *Failure {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &Failure{Status: http.StatusNotFound, Message: kind + " not found", Detail: err.Error()}
	}
	return &Failure{Status: http.StatusInternalServerError, Message: kind + " lookup failed", Detail: err.Error()}
}

func dependencyFailure(name string) *Failure {
	return &Failure{
		Status:  http.StatusInternalServerError,
		Message: "Authorization dependency unavailable",
		Detail:  name + " hook is not configured",
	}
}

func CurrentUser(c echo.Context) (*models.User, error) {
	cognitoSub, err := cognitoSubFromContext(c)
	if err != nil {
		return nil, err
	}
	user, err := currentUserByCognitoSub(cognitoSub)
	if err != nil {
		return nil, err
	}
	return scopeUserToTenant(c, user), nil
}

// A tenant app client is an authenticated organizational entry point, not a
// cosmetic hostname. Platform-root authority exists on EventiApp and the ITBEM
// control plane; branded customer portals use explicit memberships and roles.
func scopeUserToTenant(c echo.Context, user *models.User) *models.User {
	tenantCode, _ := c.Get("tenant_code").(string)
	if user == nil || tenantCode == "" ||
		strings.EqualFold(tenantCode, "eventiapp") ||
		strings.EqualFold(tenantCode, "itbem") {
		return user
	}
	scoped := *user
	scoped.IsRoot = false
	scoped.RootLevel = models.RootLevelNone
	scoped.AuthTenantCode = strings.ToLower(strings.TrimSpace(tenantCode))
	return &scoped
}

func cognitoSubFromContext(c echo.Context) (string, error) {
	cognitoSub, ok := c.Get("cognito_sub").(string)
	if !ok || cognitoSub == "" {
		return "", &Failure{Status: http.StatusUnauthorized, Message: "Unauthorized", Detail: "Invalid token"}
	}
	return cognitoSub, nil
}

func currentUserByCognitoSub(cognitoSub string) (*models.User, error) {
	if hooks.SyncUser == nil {
		return nil, dependencyFailure("SyncUser")
	}

	user, err := hooks.SyncUser(cognitoSub)
	if err != nil {
		return nil, &Failure{Status: http.StatusUnauthorized, Message: "User not found", Detail: err.Error()}
	}
	return user, nil
}

func RequireRoot(c echo.Context) (*models.User, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, err
	}
	if !user.IsPlatformAdmin() {
		return nil, &Failure{
			Status:  http.StatusForbidden,
			Message: "Forbidden",
			Detail:  "Admin only",
		}
	}
	return user, nil
}

// RequirePrimaryRoot gates platform-governance actions. Level 2 remains an
// operational administrator, never a superuser.
func RequirePrimaryRoot(c echo.Context) (*models.User, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, err
	}
	if !user.IsPrimaryRoot() {
		return nil, &Failure{Status: http.StatusForbidden, Message: "Forbidden", Detail: "Primary platform administrator required"}
	}
	return user, nil
}

func RequireClientAccess(user *models.User, clientID uuid.UUID) error {
	if err := requireTenantClientBoundary(user, clientID); err != nil {
		return err
	}
	if user.IsPlatformAdmin() {
		return nil
	}
	if hooks.CheckAccessRecursive == nil {
		return dependencyFailure("CheckAccessRecursive")
	}
	allowed, _ := hooks.CheckAccessRecursive(user.ID, clientID)
	if !allowed {
		return &Failure{
			Status:  http.StatusForbidden,
			Message: "Access denied",
			Detail:  "You do not have access to this client",
		}
	}
	return nil
}

// RequireClientCapability evaluates the effective direct or inherited client
// role. Event memberships may narrow this later, but can never grant a
// capability beyond this organization role.
func RequireClientCapability(user *models.User, clientID uuid.UUID, capability Capability) error {
	if err := requireTenantClientBoundary(user, clientID); err != nil {
		return err
	}
	if user.IsPlatformAdmin() {
		if platformHasCapability(user, capability) {
			return nil
		}
		return &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your platform administrator level cannot perform this action"}
	}
	if hooks.CheckAccessRecursive == nil {
		return dependencyFailure("CheckAccessRecursive")
	}
	allowed, role := hooks.CheckAccessRecursive(user.ID, clientID)
	if !allowed || !roleHasCapability(role, capability) {
		return &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your organization role cannot perform this action"}
	}
	return nil
}

func requireTenantClientBoundary(user *models.User, clientID uuid.UUID) error {
	if user == nil || strings.TrimSpace(user.AuthTenantCode) == "" || strings.EqualFold(user.AuthTenantCode, "eventiapp") {
		return nil
	}
	if hooks.GetClientByID == nil {
		return dependencyFailure("GetClientByID")
	}
	currentID := clientID
	for depth := 0; depth < 32 && currentID != uuid.Nil; depth++ {
		client, err := hooks.GetClientByID(currentID)
		if err != nil || client == nil {
			return &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Client is outside the authenticated tenant"}
		}
		if strings.EqualFold(strings.TrimSpace(client.Code), user.AuthTenantCode) {
			return nil
		}
		if client.ParentID == nil {
			break
		}
		currentID = *client.ParentID
	}
	return &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Client is outside the authenticated tenant"}
}

// platformHasCapability makes Root 2 useful for cross-account operational
// support without treating it as a second superuser. Root 1 remains the only
// level allowed to change event structure, teams, or organization governance.
func platformHasCapability(user *models.User, capability Capability) bool {
	if user.IsPrimaryRoot() {
		return true
	}
	if user.EffectiveRootLevel() != models.RootLevelOperational {
		return false
	}
	switch capability {
	case CapabilityView, CapabilityGuestManage, CapabilityCheckin, CapabilityAnalyticsView, CapabilityMembersManage:
		return true
	default:
		return false
	}
}

func roleHasCapability(role string, capability Capability) bool {
	code := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(role)), "INHERITED_")
	if code == "OWNER" || code == "ADMIN" {
		return true
	}
	switch capability {
	case CapabilityView:
		return code != ""
	case CapabilityEventManage:
		return code == "EVENT_MANAGER" || code == "EDITOR" || code == "MEMBER"
	case CapabilityGuestManage:
		return code == "EVENT_MANAGER" || code == "EDITOR" || code == "MEMBER"
	case CapabilityCheckin:
		return code == "EVENT_MANAGER" || code == "EDITOR" || code == "MEMBER" || code == "CHECKIN"
	case CapabilityAnalyticsView:
		return code == "EVENT_MANAGER" || code == "EDITOR" || code == "MEMBER" || code == "ANALYST"
	case CapabilityMembersManage, CapabilityOrgManage:
		return false
	default:
		return false
	}
}

func RequireEventAccess(c echo.Context, eventID uuid.UUID) (*models.User, *models.Event, error) {
	cognitoSub, err := cognitoSubFromContext(c)
	if err != nil {
		return nil, nil, err
	}

	user, event, err := loadEventAccessInputs(
		func() (*models.User, error) {
			user, loadErr := currentUserByCognitoSub(cognitoSub)
			return scopeUserToTenant(c, user), loadErr
		},
		func() (*models.Event, error) {
			return lookupEventByID(eventID)
		},
	)
	if err != nil {
		return nil, nil, err
	}
	if err := EnsureEventAccess(user, event); err != nil {
		return nil, nil, err
	}
	return user, event, nil
}

func EnsureEventIDAccess(user *models.User, eventID uuid.UUID) (*models.Event, error) {
	event, err := lookupEventByID(eventID)
	if err != nil {
		return nil, err
	}
	if err := EnsureEventAccess(user, event); err != nil {
		return nil, err
	}
	return event, nil
}

func lookupEventByID(eventID uuid.UUID) (*models.Event, error) {
	if hooks.GetEventByIDRaw == nil {
		return nil, dependencyFailure("GetEventByIDRaw")
	}
	event, err := hooks.GetEventByIDRaw(eventID)
	if err != nil {
		return nil, lookupFailure("Event", err)
	}
	return event, nil
}

// loadEventAccessInputs overlaps the two independent inputs needed before
// event authorization: identity synchronization and the event row lookup.
// The fan-out is always two, errors retain user-first priority, and the caller
// performs the client-membership check only after both reads have joined.
func loadEventAccessInputs(
	loadUser func() (*models.User, error),
	loadEvent func() (*models.Event, error),
) (*models.User, *models.Event, error) {
	type userResult struct {
		user       *models.User
		err        error
		panicValue any
	}
	type eventResult struct {
		event      *models.Event
		err        error
		panicValue any
	}

	// Buffers let the secondary worker finish even when a primary failure
	// returns before its result is consumed.
	userResults := make(chan userResult, 1)
	eventResults := make(chan eventResult, 1)
	go func() {
		result := userResult{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.panicValue = panicValue
			}
			userResults <- result
		}()
		result.user, result.err = loadUser()
	}()
	go func() {
		result := eventResult{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.panicValue = panicValue
			}
			eventResults <- result
		}()
		result.event, result.err = loadEvent()
	}()

	user := <-userResults
	// Re-panic on the request goroutine so Echo's recovery middleware can
	// contain dependency panics instead of letting a worker crash the process.
	if user.panicValue != nil {
		panic(user.panicValue)
	}
	if user.err != nil {
		return nil, nil, user.err
	}

	event := <-eventResults
	if event.panicValue != nil {
		panic(event.panicValue)
	}
	if event.err != nil {
		return nil, nil, event.err
	}
	return user.user, event.event, nil
}

func EnsureEventAccess(user *models.User, event *models.Event) error {
	if user.IsPlatformAdmin() {
		return nil
	}
	if event.ClientID == nil {
		return &Failure{
			Status:  http.StatusForbidden,
			Message: "Access denied",
			Detail:  "Event is not linked to a client",
		}
	}
	return RequireClientAccess(user, *event.ClientID)
}

func RequireEventCapability(c echo.Context, eventID uuid.UUID, capability Capability) (*models.User, *models.Event, error) {
	user, event, err := RequireEventAccess(c, eventID)
	if err != nil {
		return nil, nil, err
	}
	if user.IsPlatformAdmin() {
		if platformHasCapability(user, capability) {
			return user, event, nil
		}
		return nil, nil, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your platform administrator level cannot perform this action"}
	}
	if event.ClientID == nil {
		return nil, nil, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Event is not linked to a client"}
	}
	if err := RequireClientCapability(user, *event.ClientID, capability); err != nil {
		return nil, nil, err
	}
	if hooks.GetEventMemberRole != nil {
		role, found, memberErr := hooks.GetEventMemberRole(eventID, user.ID)
		if memberErr != nil {
			return nil, nil, &Failure{Status: http.StatusInternalServerError, Message: "Authorization error", Detail: memberErr.Error()}
		}
		if found && !eventRoleHasCapability(role, capability) {
			return nil, nil, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your event assignment cannot perform this action"}
		}
	}
	return user, event, nil
}

func eventRoleHasCapability(role string, capability Capability) bool {
	code := strings.ToUpper(strings.TrimSpace(role))
	if code == "EVENT_OWNER" || code == "OWNER" || code == "MANAGER" || code == "EVENT_MANAGER" {
		return true
	}
	switch capability {
	case CapabilityView:
		return code == "EDITOR" || code == "CHECKIN" || code == "ANALYST" || code == "VIEWER"
	case CapabilityEventManage:
		return code == "EDITOR"
	case CapabilityGuestManage:
		return code == "EDITOR"
	case CapabilityCheckin:
		return code == "EDITOR" || code == "CHECKIN"
	case CapabilityAnalyticsView:
		return code == "EDITOR" || code == "ANALYST"
	default:
		return false
	}
}

func RequireEventSectionAccess(c echo.Context, sectionID uuid.UUID) (*models.User, *models.EventSection, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, nil, err
	}

	section, err := EnsureEventSectionAccess(user, sectionID)
	if err != nil {
		return nil, nil, err
	}
	return user, section, nil
}

func RequireEventSectionCapability(c echo.Context, sectionID uuid.UUID, capability Capability) (*models.User, *models.EventSection, error) {
	user, section, err := RequireEventSectionAccess(c, sectionID)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := RequireEventCapability(c, section.EventID, capability); err != nil {
		return nil, nil, err
	}
	return user, section, nil
}

func EnsureEventSectionAccess(user *models.User, sectionID uuid.UUID) (*models.EventSection, error) {
	if hooks.GetEventSectionByID == nil {
		return nil, dependencyFailure("GetEventSectionByID")
	}
	section, err := hooks.GetEventSectionByID(sectionID)
	if err != nil {
		return nil, lookupFailure("Event section", err)
	}
	if _, err := EnsureEventIDAccess(user, section.EventID); err != nil {
		return nil, err
	}
	return section, nil
}

func RequireMomentAccess(c echo.Context, momentID uuid.UUID) (*models.User, *models.Moment, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, nil, err
	}

	moment, err := EnsureMomentAccess(user, momentID)
	if err != nil {
		return nil, nil, err
	}
	return user, moment, nil
}

func RequireMomentCapability(c echo.Context, momentID uuid.UUID, capability Capability) (*models.User, *models.Moment, error) {
	user, moment, err := RequireMomentAccess(c, momentID)
	if err != nil {
		return nil, nil, err
	}
	if user.IsPlatformAdmin() {
		if platformHasCapability(user, capability) {
			return user, moment, nil
		}
		return nil, nil, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Your platform administrator level cannot perform this action"}
	}
	if moment.EventID == nil {
		return nil, nil, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Moment is not linked to an event"}
	}
	if _, _, err := RequireEventCapability(c, *moment.EventID, capability); err != nil {
		return nil, nil, err
	}
	return user, moment, nil
}

func EnsureMomentAccess(user *models.User, momentID uuid.UUID) (*models.Moment, error) {
	if hooks.GetMomentByID == nil {
		return nil, dependencyFailure("GetMomentByID")
	}
	moment, err := hooks.GetMomentByID(momentID)
	if err != nil {
		return nil, lookupFailure("Moment", err)
	}
	if user.IsPlatformAdmin() {
		return moment, nil
	}
	if moment.EventID == nil {
		return nil, &Failure{
			Status:  http.StatusForbidden,
			Message: "Access denied",
			Detail:  "Moment is not linked to an event",
		}
	}

	if _, err := EnsureEventIDAccess(user, *moment.EventID); err != nil {
		return nil, err
	}
	return moment, nil
}

func RequireGuestAccess(c echo.Context, guestID uuid.UUID) (*models.User, *models.Guest, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, nil, err
	}

	guest, err := EnsureGuestAccess(user, guestID)
	if err != nil {
		return nil, nil, err
	}
	return user, guest, nil
}

func RequireGuestCapability(c echo.Context, guestID uuid.UUID, capability Capability) (*models.User, *models.Guest, error) {
	user, guest, err := RequireGuestAccess(c, guestID)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := RequireEventCapability(c, guest.EventID, capability); err != nil {
		return nil, nil, err
	}
	return user, guest, nil
}

func EnsureGuestAccess(user *models.User, guestID uuid.UUID) (*models.Guest, error) {
	if hooks.GetGuestByID == nil {
		return nil, dependencyFailure("GetGuestByID")
	}
	guest, err := hooks.GetGuestByID(guestID)
	if err != nil {
		return nil, lookupFailure("Guest", err)
	}
	if guest.EventID == uuid.Nil {
		return nil, &Failure{
			Status:  http.StatusForbidden,
			Message: "Access denied",
			Detail:  "Guest is not linked to an event",
		}
	}
	if _, err := EnsureEventIDAccess(user, guest.EventID); err != nil {
		return nil, err
	}
	return guest, nil
}

func RequireInvitationAccess(c echo.Context, invitationID uuid.UUID) (*models.User, *models.Invitation, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, nil, err
	}

	if hooks.GetInvitationByIDLite == nil {
		return nil, nil, dependencyFailure("GetInvitationByIDLite")
	}
	invitation, err := hooks.GetInvitationByIDLite(invitationID)
	if err != nil {
		return nil, nil, lookupFailure("Invitation", err)
	}
	if invitation.EventID == uuid.Nil {
		return nil, nil, &Failure{
			Status:  http.StatusForbidden,
			Message: "Access denied",
			Detail:  "Invitation is not linked to an event",
		}
	}
	if _, err := EnsureEventIDAccess(user, invitation.EventID); err != nil {
		return nil, nil, err
	}
	return user, invitation, nil
}

func RequireInvitationCapability(c echo.Context, invitationID uuid.UUID, capability Capability) (*models.User, *models.Invitation, error) {
	user, invitation, err := RequireInvitationAccess(c, invitationID)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := RequireEventCapability(c, invitation.EventID, capability); err != nil {
		return nil, nil, err
	}
	return user, invitation, nil
}

func RequireResourceAccess(c echo.Context, resourceID uuid.UUID) (*models.User, *models.Resource, error) {
	user, err := CurrentUser(c)
	if err != nil {
		return nil, nil, err
	}

	if hooks.GetResourceByID == nil {
		return nil, nil, dependencyFailure("GetResourceByID")
	}
	resource, err := hooks.GetResourceByID(resourceID)
	if err != nil {
		return nil, nil, lookupFailure("Resource", err)
	}
	if resource.EventSectionID == nil {
		if user.IsPlatformAdmin() {
			return user, resource, nil
		}
		return nil, nil, &Failure{
			Status:  http.StatusForbidden,
			Message: "Access denied",
			Detail:  "Resource is not linked to an event section",
		}
	}
	if _, err := EnsureEventSectionAccess(user, *resource.EventSectionID); err != nil {
		return nil, nil, err
	}
	return user, resource, nil
}

// RequireResourceCapability applies the same capability ceiling as other
// event mutations. Resources without a section are platform-owned assets and
// may only be mutated by the primary platform administrator.
func RequireResourceCapability(c echo.Context, resourceID uuid.UUID, capability Capability) (*models.User, *models.Resource, error) {
	user, resource, err := RequireResourceAccess(c, resourceID)
	if err != nil {
		return nil, nil, err
	}
	if resource.EventSectionID == nil {
		if user.IsPrimaryRoot() {
			return user, resource, nil
		}
		return nil, nil, &Failure{Status: http.StatusForbidden, Message: "Access denied", Detail: "Only the primary platform administrator can mutate platform resources"}
	}
	if _, _, err := RequireEventSectionCapability(c, *resource.EventSectionID, capability); err != nil {
		return nil, nil, err
	}
	return user, resource, nil
}

func RequireEventClientForCreate(user *models.User, clientID *uuid.UUID) error {
	if clientID == nil {
		return &Failure{
			Status:  http.StatusBadRequest,
			Message: "client_id required",
			Detail:  "Events must belong to a client",
		}
	}
	if err := RequireClientCapability(user, *clientID, CapabilityEventManage); err != nil {
		return err
	}
	return nil
}

func RequireEventSectionForCreate(user *models.User, eventID uuid.UUID) error {
	if eventID == uuid.Nil {
		return &Failure{
			Status:  http.StatusBadRequest,
			Message: "event_id required",
			Detail:  "Event sections must belong to an event",
		}
	}
	if _, err := EnsureEventIDAccess(user, eventID); err != nil {
		return err
	}
	return nil
}

func RequireResourceSectionForCreate(user *models.User, sectionID *uuid.UUID) error {
	if sectionID == nil {
		if user.IsPrimaryRoot() {
			return nil
		}
		return &Failure{
			Status:  http.StatusBadRequest,
			Message: "section_id required",
			Detail:  "Resources created from the dashboard must belong to an event section",
		}
	}
	if _, err := EnsureEventSectionAccess(user, *sectionID); err != nil {
		return err
	}
	return nil
}

func RequireClientMoveAccess(user *models.User, oldClientID, newClientID *uuid.UUID) error {
	if newClientID == nil {
		return nil
	}
	if oldClientID != nil && *oldClientID == *newClientID {
		return nil
	}
	if err := RequireClientCapability(user, *newClientID, CapabilityEventManage); err != nil {
		return err
	}
	return nil
}

func InvalidUUID(kind string, err error) *Failure {
	return &Failure{
		Status:  http.StatusBadRequest,
		Message: fmt.Sprintf("Invalid %s", kind),
		Detail:  err.Error(),
	}
}
