// Package eventclients manages EventiApp event clients: external accounts
// that a manager assigns to individual events (event_client_grants). The
// controller authorizes the caller first; this service owns identity
// resolution, eligibility and the grant writes.
package eventclients

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"events-stocks/dtos"
	"events-stocks/internal/products"
	"events-stocks/models"
	"events-stocks/repositories/eventclientrepository"

	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/gofrs/uuid"
	"gorm.io/gorm"
)

const (
	AccountCreated  = "created"
	AccountExisting = "existing"

	maxAssignmentIDs     = 100
	maxPageSize          = 50
	defaultPageSize      = 20
	candidateLimit       = 50
	cognitoPendingInvite = "FORCE_CHANGE_PASSWORD"
)

// Error is a domain error with a Spanish message and a stable code.
type Error struct {
	Status  int
	Message string
	Code    string
	Data    interface{}
}

func (e *Error) Error() string { return e.Code }

var (
	ErrInvalidRequest       = &Error{Status: http.StatusBadRequest, Message: "Datos inválidos", Code: "invalid_request"}
	ErrInvalidEmail         = &Error{Status: http.StatusBadRequest, Message: "Correo inválido", Code: "invalid_email"}
	ErrNotFound             = &Error{Status: http.StatusNotFound, Message: "Cliente no encontrado", Code: "event_client_not_found"}
	ErrIsOrganizationMember = &Error{Status: http.StatusConflict, Message: "Esta persona pertenece o perteneció a la organización del evento. Gestiona su acceso desde Equipo.", Code: "event_client_is_org_member"}
	ErrSelf                 = &Error{Status: http.StatusConflict, Message: "No puedes asignarte a ti mismo como cliente.", Code: "event_client_self"}
	// ErrUnavailable deliberately has one body for every cause (platform
	// administrator, inactive, unverified or changed email, ambiguous email,
	// unresolvable account, event organization outside EventiApp) so a manager
	// cannot enumerate accounts.
	ErrUnavailable          = &Error{Status: http.StatusConflict, Message: "No podemos asignar esta cuenta como cliente. Verifica el correo o contacta a soporte.", Code: "event_client_unavailable"}
	ErrInvitationNotPending = &Error{Status: http.StatusConflict, Message: "Esta persona ya activó su cuenta; no hay invitación pendiente.", Code: "event_client_invitation_not_pending"}
	ErrInviteFailed         = &Error{Status: http.StatusInternalServerError, Message: "No se pudo crear la cuenta del cliente.", Code: "event_client_invite_failed"}
	ErrGrantFailed          = &Error{Status: http.StatusInternalServerError, Message: "No se pudo asignar el evento al cliente.", Code: "event_client_grant_failed"}
	ErrResendFailed         = &Error{Status: http.StatusInternalServerError, Message: "No se pudo reenviar la invitación.", Code: "event_client_resend_failed"}
	// ErrForbidden is mapped by the controller to the authz 403 shape.
	ErrForbidden = errors.New("event client assignment denied")
)

func errOutsideOrganization(ids []uuid.UUID) *Error {
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Message: "Uno o más eventos no pertenecen a esta organización.",
		Code:    "event_outside_organization",
		Data:    map[string]interface{}{"event_ids": ids},
	}
}

// Store is the grant persistence used by the service.
type Store interface {
	Transaction(fn func(tx *gorm.DB) error) error
	ListActiveForEvent(eventID uuid.UUID) ([]eventclientrepository.EventClientRow, error)
	ActiveGrantForEvent(eventID, userID uuid.UUID) (*eventclientrepository.EventClientRow, error)
	ListOrganizationClients(orgID, eventID uuid.UUID, search string, limit int) ([]eventclientrepository.CandidateRow, error)
	IsOrganizationClient(orgID, userID uuid.UUID) (bool, error)
	ListOrganizationEventsForClient(orgID, userID uuid.UUID, search string, page, pageSize int) ([]eventclientrepository.AssignmentRow, int, error)
	EventOrganizations(eventIDs []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error)
	FindLiveUsersByEmail(email string, limit int) ([]eventclientrepository.LiveUser, error)
	HasMembershipInOrganizationTree(orgID, userID uuid.UUID) (bool, error)
	Grant(tx *gorm.DB, eventID, userID, orgID, actorID uuid.UUID) (bool, *models.EventClientGrant, error)
	Revoke(tx *gorm.DB, eventID, userID, actorID uuid.UUID) (bool, error)
	RevokeAllForOrganizationUser(tx *gorm.DB, orgID, userID, actorID uuid.UUID) (int64, error)
	ListActiveForOrganization(orgID uuid.UUID, page, pageSize int) ([]eventclientrepository.OrganizationGrantRow, int, error)
	CountActiveForUserInOrganization(orgID, userID uuid.UUID) (int, error)
}

// Identity is the account side: local users and the Cognito pool.
type Identity interface {
	InviteUserForTenant(email, firstName, lastName, tenantCode string) (*models.User, error)
	SyncUser(cognitoSub string) (*models.User, error)
	DeleteFullAccount(cognitoSub string) error
	GetUserByID(id uuid.UUID) (*models.User, error)
	// GetAuthUser reads the pool account by username, alias or sub.
	GetAuthUser(username string) (*dtos.AuthUser, error)
	ResendInvitation(username string) error
}

// Organizations resolves organization membership and product.
type Organizations interface {
	CheckAccessRecursiveE(userID, clientID uuid.UUID) (bool, string, error)
	ResolveClientProduct(clientID uuid.UUID) (string, error)
}

// CapabilityBatch evaluates event:clients:manage of the actor on events of
// one organization (authz.EventCapabilityForOrganizationEvents).
type CapabilityBatch func(actor *models.User, orgID uuid.UUID, eventIDs []uuid.UUID) (map[uuid.UUID]bool, error)

type Service struct {
	store         Store
	identity      Identity
	organizations Organizations
	canManage     CapabilityBatch
	invalidate    func(cognitoSub string)
}

func NewService(store Store, identity Identity, organizations Organizations, canManage CapabilityBatch, invalidate func(string)) *Service {
	if invalidate == nil {
		invalidate = func(string) {}
	}
	return &Service{store: store, identity: identity, organizations: organizations, canManage: canManage, invalidate: invalidate}
}

// Request carries the authenticated actor and request metadata.
type Request struct {
	Actor      *models.User
	RequestID  string
	TenantCode string
}

func (s *Service) List(eventID uuid.UUID) ([]dtos.EventClientResponse, error) {
	rows, err := s.store.ListActiveForEvent(eventID)
	if err != nil {
		return nil, err
	}
	result := make([]dtos.EventClientResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, clientResponse(row))
	}
	return result, nil
}

func (s *Service) Candidates(event *models.Event, search string) ([]dtos.EventClientCandidateResponse, error) {
	orgID, err := eventOrganization(event)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListOrganizationClients(orgID, event.ID, search, candidateLimit)
	if err != nil {
		return nil, err
	}
	result := make([]dtos.EventClientCandidateResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, dtos.EventClientCandidateResponse(row))
	}
	return result, nil
}

// Create assigns a client to the event, inviting a new account when needed.
// It returns the response and whether a new grant row was created.
func (s *Service) Create(req Request, event *models.Event, body dtos.EventClientCreateRequest) (*dtos.EventClientCreateResponse, bool, error) {
	orgID, err := eventOrganization(event)
	if err != nil {
		return nil, false, err
	}
	byEmail := body.Email != nil || body.FirstName != nil || body.LastName != nil
	byUserID := body.UserID != nil
	if byEmail == byUserID {
		return nil, false, ErrInvalidRequest
	}
	// The product boundary only depends on the event: check it before any
	// account is invited.
	if err := s.requireEventiAppOrganization(req, orgID); err != nil {
		return nil, false, err
	}

	var target *models.User
	account := AccountExisting
	freshAccount := false
	if byUserID {
		userID, parseErr := uuid.FromString(strings.TrimSpace(*body.UserID))
		if parseErr != nil {
			return nil, false, ErrInvalidRequest
		}
		known, lookupErr := s.store.IsOrganizationClient(orgID, userID)
		if lookupErr != nil {
			return nil, false, lookupErr
		}
		if !known {
			return nil, false, ErrNotFound
		}
		target, err = s.identity.GetUserByID(userID)
		if err != nil || target == nil {
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, false, err
			}
			return nil, false, ErrNotFound
		}
	} else {
		if body.Email == nil || body.FirstName == nil || body.LastName == nil {
			return nil, false, ErrInvalidRequest
		}
		email, ok := normalizeEmail(*body.Email)
		if !ok {
			return nil, false, ErrInvalidEmail
		}
		firstName, lastName := strings.TrimSpace(*body.FirstName), strings.TrimSpace(*body.LastName)
		if !validName(firstName) || !validName(lastName) {
			return nil, false, ErrInvalidRequest
		}
		target, freshAccount, err = s.resolveIdentity(req, email, firstName, lastName)
		if err != nil {
			return nil, false, err
		}
		if freshAccount {
			account = AccountCreated
		}
	}

	if err := s.checkEligibility(req, orgID, target); err != nil {
		s.rollbackFreshAccount(req, event.ID, target, freshAccount)
		return nil, false, err
	}

	var created bool
	err = s.store.Transaction(func(tx *gorm.DB) error {
		var grantErr error
		created, _, grantErr = s.store.Grant(tx, event.ID, target.ID, orgID, req.Actor.ID)
		return grantErr
	})
	if err != nil {
		slog.Error("event client grant failed", "request_id", req.RequestID, "event_id", event.ID, "user_id", target.ID, "error", err)
		s.rollbackFreshAccount(req, event.ID, target, freshAccount)
		return nil, false, ErrGrantFailed
	}
	s.invalidate(target.CognitoSub)

	row, err := s.store.ActiveGrantForEvent(event.ID, target.ID)
	if err != nil {
		return nil, false, err
	}
	response := &dtos.EventClientCreateResponse{Account: account, AlreadyAssigned: !created}
	if row != nil {
		response.Client = clientResponse(*row)
	} else {
		response.Client = dtos.EventClientResponse{UserID: target.ID, Email: target.Email, FirstName: target.FirstName, LastName: target.LastName, GrantedByUserID: req.Actor.ID}
	}
	return response, created, nil
}

// resolveIdentity implements the hardened identity rules: exactly one live
// local user with a matching, verified, enabled Cognito email; otherwise a new
// invitation; on UsernameExists the Cognito account is resolved by email and
// mirrored locally. Every failure collapses into ErrUnavailable.
func (s *Service) resolveIdentity(req Request, email, firstName, lastName string) (*models.User, bool, error) {
	rows, err := s.store.FindLiveUsersByEmail(email, 2)
	if err != nil {
		return nil, false, err
	}
	switch len(rows) {
	case 2:
		slog.Warn("event client email is ambiguous", "request_id", req.RequestID)
		return nil, false, ErrUnavailable
	case 1:
		authUser, authErr := s.identity.GetAuthUser(rows[0].CognitoSub)
		if authErr != nil || !cognitoIdentityMatches(authUser, email) {
			slog.Warn("event client identity unavailable", "request_id", req.RequestID, "reason", "cognito_mismatch")
			return nil, false, ErrUnavailable
		}
		user, userErr := s.identity.GetUserByID(rows[0].ID)
		if userErr != nil || user == nil {
			return nil, false, ErrUnavailable
		}
		return user, false, nil
	}

	tenant := strings.TrimSpace(req.TenantCode)
	if tenant == "" {
		tenant = string(products.DefaultCode)
	}
	user, inviteErr := s.identity.InviteUserForTenant(email, firstName, lastName, tenant)
	if inviteErr == nil {
		return user, true, nil
	}
	if !isUsernameExists(inviteErr) {
		slog.Error("event client invitation failed", "request_id", req.RequestID, "error", inviteErr)
		return nil, false, ErrInviteFailed
	}
	authUser, authErr := s.identity.GetAuthUser(email)
	if authErr != nil || !cognitoIdentityMatches(authUser, email) {
		slog.Warn("event client identity unavailable", "request_id", req.RequestID, "reason", "existing_unresolved")
		return nil, false, ErrUnavailable
	}
	subject := strings.TrimSpace(authUser.Subject)
	if subject == "" {
		subject = strings.TrimSpace(authUser.Sub)
	}
	user, syncErr := s.identity.SyncUser(subject)
	if syncErr != nil || user == nil {
		slog.Warn("event client identity unavailable", "request_id", req.RequestID, "reason", "sync_failed")
		return nil, false, ErrUnavailable
	}
	return user, false, nil
}

func cognitoIdentityMatches(authUser *dtos.AuthUser, email string) bool {
	return authUser != nil && authUser.IsActive && authUser.EmailVerified && strings.EqualFold(strings.TrimSpace(authUser.Email), email)
}

func isUsernameExists(err error) bool {
	var exists *types.UsernameExistsException
	if errors.As(err, &exists) {
		return true
	}
	return strings.Contains(err.Error(), "UsernameExistsException")
}

// checkEligibility rejects self-assignment, platform administrators, inactive
// accounts and anyone with any membership (active or not) in the event's
// organization tree or with organization access to it.
func (s *Service) checkEligibility(req Request, orgID uuid.UUID, target *models.User) error {
	if target.ID == req.Actor.ID {
		return ErrSelf
	}
	if target.IsPlatformAdmin() || !target.IsActive {
		slog.Warn("event client target unavailable", "request_id", req.RequestID, "reason", "platform_or_inactive")
		return ErrUnavailable
	}
	member, err := s.store.HasMembershipInOrganizationTree(orgID, target.ID)
	if err != nil {
		return err
	}
	if member {
		return ErrIsOrganizationMember
	}
	allowed, _, err := s.organizations.CheckAccessRecursiveE(target.ID, orgID)
	if err != nil {
		return err
	}
	if allowed {
		return ErrIsOrganizationMember
	}
	return nil
}

func (s *Service) requireEventiAppOrganization(req Request, orgID uuid.UUID) error {
	product, err := s.organizations.ResolveClientProduct(orgID)
	if err != nil {
		return err
	}
	if !products.SupportsEventOperations(product) {
		slog.Warn("event client target unavailable", "request_id", req.RequestID, "reason", "organization_product")
		return ErrUnavailable
	}
	return nil
}

func (s *Service) rollbackFreshAccount(req Request, eventID uuid.UUID, target *models.User, fresh bool) {
	if !fresh || target == nil {
		return
	}
	if err := s.identity.DeleteFullAccount(target.CognitoSub); err != nil {
		slog.Error("event client account rollback failed", "request_id", req.RequestID, "event_id", eventID, "user_id", target.ID, "error", err)
	}
}

// Revoke ends the client's access to the event. The account is never deleted.
func (s *Service) Revoke(req Request, eventID, userID uuid.UUID) error {
	var revoked bool
	err := s.store.Transaction(func(tx *gorm.DB) error {
		var revokeErr error
		revoked, revokeErr = s.store.Revoke(tx, eventID, userID, req.Actor.ID)
		return revokeErr
	})
	if err != nil {
		return err
	}
	if !revoked {
		return ErrNotFound
	}
	s.invalidateUser(userID)
	return nil
}

// ResendInvitation re-sends the standard EventiApp Cognito invitation to a
// client of this event whose account still waits for its first sign-in.
func (s *Service) ResendInvitation(req Request, eventID, userID uuid.UUID) error {
	row, err := s.store.ActiveGrantForEvent(eventID, userID)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrNotFound
	}
	user, err := s.identity.GetUserByID(userID)
	if err != nil || user == nil {
		return ErrNotFound
	}
	authUser, err := s.identity.GetAuthUser(user.CognitoSub)
	if err != nil || authUser == nil {
		slog.Warn("event client resend lookup failed", "request_id", req.RequestID, "event_id", eventID, "user_id", userID)
		return ErrResendFailed
	}
	if authUser.Status != cognitoPendingInvite {
		return ErrInvitationNotPending
	}
	username := strings.TrimSpace(authUser.Username)
	if username == "" {
		username = user.Email
	}
	if err := s.identity.ResendInvitation(username); err != nil {
		slog.Error("event client invitation resend failed", "request_id", req.RequestID, "event_id", eventID, "user_id", userID, "error", err)
		return ErrResendFailed
	}
	return nil
}

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func (s *Service) ListAssignments(req Request, event *models.Event, userID uuid.UUID, search string, page, pageSize int) (dtos.EventClientPage[dtos.EventClientAssignmentResponse], error) {
	var empty dtos.EventClientPage[dtos.EventClientAssignmentResponse]
	orgID, err := eventOrganization(event)
	if err != nil {
		return empty, err
	}
	known, err := s.store.IsOrganizationClient(orgID, userID)
	if err != nil {
		return empty, err
	}
	if !known {
		return empty, ErrNotFound
	}
	page, pageSize = normalizePage(page, pageSize)
	rows, total, err := s.store.ListOrganizationEventsForClient(orgID, userID, search, page, pageSize)
	if err != nil {
		return empty, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EventID)
	}
	manageable, err := s.canManage(req.Actor, orgID, ids)
	if err != nil {
		return empty, err
	}
	items := make([]dtos.EventClientAssignmentResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, dtos.EventClientAssignmentResponse{
			EventID: row.EventID, Name: row.Name, EventDateTime: row.EventDateTime, Timezone: row.Timezone,
			IsActive: row.IsActive, Assigned: row.Assigned, CanManage: manageable[row.EventID],
		})
	}
	return dtos.NewEventClientPage(items, total, page, pageSize), nil
}

func parseIDList(raw []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(raw))
	seen := make(map[uuid.UUID]struct{}, len(raw))
	for _, value := range raw {
		id, err := uuid.FromString(strings.TrimSpace(value))
		if err != nil || id == uuid.Nil {
			return nil, ErrInvalidRequest
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// UpdateAssignments assigns and unassigns events of the anchor event's
// organization for one client, all or nothing. Every check runs before any
// write.
func (s *Service) UpdateAssignments(req Request, event *models.Event, userID uuid.UUID, body dtos.EventClientAssignmentsRequest) (*dtos.EventClientAssignmentsUpdateResponse, error) {
	orgID, err := eventOrganization(event)
	if err != nil {
		return nil, err
	}
	assign, err := parseIDList(body.Assign)
	if err != nil {
		return nil, err
	}
	unassign, err := parseIDList(body.Unassign)
	if err != nil {
		return nil, err
	}
	total := len(assign) + len(unassign)
	if total == 0 || total > maxAssignmentIDs {
		return nil, ErrInvalidRequest
	}
	assignSet := make(map[uuid.UUID]struct{}, len(assign))
	for _, id := range assign {
		assignSet[id] = struct{}{}
	}
	for _, id := range unassign {
		if _, overlap := assignSet[id]; overlap {
			return nil, ErrInvalidRequest
		}
	}
	known, err := s.store.IsOrganizationClient(orgID, userID)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, ErrNotFound
	}
	all := append(append([]uuid.UUID{}, assign...), unassign...)
	owners, err := s.store.EventOrganizations(all)
	if err != nil {
		return nil, err
	}
	outside := make([]uuid.UUID, 0)
	for _, id := range all {
		owner, ok := owners[id]
		if !ok || owner == nil || *owner != orgID {
			outside = append(outside, id)
		}
	}
	if len(outside) > 0 {
		return nil, errOutsideOrganization(outside)
	}
	manageable, err := s.canManage(req.Actor, orgID, all)
	if err != nil {
		return nil, err
	}
	for _, id := range all {
		if !manageable[id] {
			return nil, ErrForbidden
		}
	}
	target, err := s.identity.GetUserByID(userID)
	if err != nil || target == nil {
		return nil, ErrNotFound
	}
	if len(assign) > 0 {
		if err := s.requireEventiAppOrganization(req, orgID); err != nil {
			return nil, err
		}
		if err := s.checkEligibility(req, orgID, target); err != nil {
			return nil, err
		}
	}
	err = s.store.Transaction(func(tx *gorm.DB) error {
		for _, id := range assign {
			if _, _, grantErr := s.store.Grant(tx, id, userID, orgID, req.Actor.ID); grantErr != nil {
				return grantErr
			}
		}
		for _, id := range unassign {
			if _, revokeErr := s.store.Revoke(tx, id, userID, req.Actor.ID); revokeErr != nil {
				return revokeErr
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("event client assignment update failed", "request_id", req.RequestID, "event_id", event.ID, "user_id", userID, "error", err)
		return nil, ErrGrantFailed
	}
	s.invalidate(target.CognitoSub)
	count, err := s.store.CountActiveForUserInOrganization(orgID, userID)
	if err != nil {
		return nil, err
	}
	return &dtos.EventClientAssignmentsUpdateResponse{Assigned: assign, Unassigned: unassign, AssignedEvents: count}, nil
}

func (s *Service) ListOrganizationGrants(orgID uuid.UUID, page, pageSize int) (dtos.EventClientPage[dtos.OrganizationEventClientGrantResponse], error) {
	page, pageSize = normalizePage(page, pageSize)
	rows, total, err := s.store.ListActiveForOrganization(orgID, page, pageSize)
	if err != nil {
		return dtos.EventClientPage[dtos.OrganizationEventClientGrantResponse]{}, err
	}
	items := make([]dtos.OrganizationEventClientGrantResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, dtos.OrganizationEventClientGrantResponse{
			GrantID: row.GrantID, EventID: row.EventID, EventName: row.EventName, EventDateTime: row.EventDateTime,
			Client:    dtos.EventClientPerson(row.Client),
			GrantedBy: dtos.EventClientPerson(row.GrantedBy),
			GrantedAt: row.GrantedAt,
		})
	}
	return dtos.NewEventClientPage(items, total, page, pageSize), nil
}

// RevokeOrganizationUser removes every grant of one person in one
// organization (offboarding). It returns the number of revoked grants.
func (s *Service) RevokeOrganizationUser(req Request, orgID, userID uuid.UUID) (int64, error) {
	var count int64
	err := s.store.Transaction(func(tx *gorm.DB) error {
		var revokeErr error
		count, revokeErr = s.store.RevokeAllForOrganizationUser(tx, orgID, userID, req.Actor.ID)
		return revokeErr
	})
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, ErrNotFound
	}
	s.invalidateUser(userID)
	return count, nil
}

func (s *Service) invalidateUser(userID uuid.UUID) {
	user, err := s.identity.GetUserByID(userID)
	if err == nil && user != nil {
		s.invalidate(user.CognitoSub)
	}
}

func eventOrganization(event *models.Event) (uuid.UUID, error) {
	if event == nil || event.ClientID == nil || *event.ClientID == uuid.Nil {
		return uuid.Nil, ErrUnavailable
	}
	return *event.ClientID, nil
}

func normalizeEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return "", false
	}
	return email, true
}

func validName(value string) bool {
	return len(value) >= 2 && len(value) <= 100
}

func clientResponse(row eventclientrepository.EventClientRow) dtos.EventClientResponse {
	return dtos.EventClientResponse(row)
}

// String helps logs and tests identify a domain error.
func (e *Error) String() string { return fmt.Sprintf("%d %s", e.Status, e.Code) }
