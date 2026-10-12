package users

import (
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"

	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
)

var ErrInvalidInviteIdentity = errors.New("invalid invitation identity")

// Guard errors for destructive admin actions on user accounts (ADM-9). The
// dashboard hides these actions, but the backend is the enforcement point.
var (
	ErrAdminUserNotFound     = errors.New("usuario no encontrado")
	ErrAdminCannotTargetSelf = errors.New("no puedes realizar esta acción sobre tu propia cuenta")
	// ErrAdminPrimaryRootProtected wraps ErrProtectedRootAccount, so every
	// primary-root refusal answers with the one contract of #285 (409
	// "Protected account", error USER_PRIMARY_ROOT_PROTECTED).
	ErrAdminPrimaryRootProtected    = fmt.Errorf("%w: una cuenta raíz principal no puede eliminarse ni desactivarse", ErrProtectedRootAccount)
	ErrAdminUserHasActiveMembership = errors.New("el usuario todavía pertenece a una o más organizaciones; quítalo de ellas antes de eliminarlo")
)

func normalizeInviteIdentity(email, firstName, lastName string) (string, string, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	firstName = strings.TrimSpace(firstName)
	lastName = strings.TrimSpace(lastName)
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return "", "", "", fmt.Errorf("%w: correo electrónico inválido", ErrInvalidInviteIdentity)
	}
	if len(firstName) < 2 || len(firstName) > 100 {
		return "", "", "", fmt.Errorf("%w: el nombre debe tener entre 2 y 100 caracteres", ErrInvalidInviteIdentity)
	}
	if len(lastName) < 2 || len(lastName) > 100 {
		return "", "", "", fmt.Errorf("%w: el apellido debe tener entre 2 y 100 caracteres", ErrInvalidInviteIdentity)
	}
	return email, firstName, lastName, nil
}

// AdminUserService is the injectable, struct-based admin user service.
type AdminUserService struct {
	userRepo   ports.UserRepository
	clientRepo ports.ClientRepository
	authRepo   ports.AuthProviderRepository
}

func NewAdminUserService(userRepo ports.UserRepository, clientRepo ports.ClientRepository, authRepo ports.AuthProviderRepository) *AdminUserService {
	return &AdminUserService{userRepo: userRepo, clientRepo: clientRepo, authRepo: authRepo}
}

func normalizeAdminUsersListQuery(query dtos.AdminUsersListQuery) dtos.AdminUsersListQuery {
	if query.PageSize <= 0 || query.PageSize > 200 {
		query.PageSize = 50
	}
	if query.Page < 1 {
		query.Page = 1
	}

	query.Search = strings.TrimSpace(query.Search)
	query.Status = strings.ToLower(strings.TrimSpace(query.Status))
	switch query.Status {
	case "", "all", "active", "inactive", "root", "non_root":
	default:
		query.Status = ""
	}

	return query
}

func (s *AdminUserService) ListAllUsers(query dtos.AdminUsersListQuery) (dtos.AdminUsersPageResponse, error) {
	query = normalizeAdminUsersListQuery(query)

	paged, total, err := s.userRepo.ListAllUsersPaginated(query)
	if err != nil {
		return dtos.AdminUsersPageResponse{}, err
	}

	userIDs := make([]uuid.UUID, len(paged))
	for i, u := range paged {
		userIDs[i] = u.ID
	}
	clientCounts := map[uuid.UUID]int64{}
	if s.clientRepo != nil && len(userIDs) > 0 {
		clientCounts, _ = s.clientRepo.CountClientsByUsers(userIDs)
	}

	return dtos.NewAdminUsersPageResponse(paged, clientCounts, total, query.Page, query.PageSize), nil
}

func (s *AdminUserService) GetUserDetail(userID uuid.UUID) (dtos.AdminUserDetailResponse, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return dtos.AdminUserDetailResponse{}, fmt.Errorf("usuario no encontrado")
	}
	clients := []models.Client{}
	if s.clientRepo != nil {
		clients, _ = s.clientRepo.ListClientsByUser(userID)
	}
	return dtos.NewAdminUserDetailResponse(user, clients), nil
}

func (s *AdminUserService) GetUserSummary(userID uuid.UUID) (dtos.AdminUserDetailResponse, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return dtos.AdminUserDetailResponse{}, fmt.Errorf("usuario no encontrado")
	}
	return dtos.NewAdminUserDetailResponse(user, nil), nil
}

func (s *AdminUserService) ListUserClients(userID uuid.UUID) ([]models.Client, error) {
	clients, err := s.clientRepo.ListClientsByUser(userID)
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func (s *AdminUserService) ListUserClientsPage(userID uuid.UUID, query dtos.ClientsListQuery) (dtos.ClientsPageResponse, error) {
	var clients []models.Client
	var total int64
	var active int64
	var inactive int64
	group := new(errgroup.Group)
	group.Go(func() error {
		loaded, count, err := s.clientRepo.ListClientsPaginated(&userID, query)
		clients, total = loaded, count
		return err
	})
	if stats, ok := s.clientRepo.(ports.UserClientStatusRepository); ok {
		group.Go(func() error {
			active, inactive, _ = stats.CountUserClientStatuses(userID)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return dtos.ClientsPageResponse{}, err
	}
	response := dtos.ClientsPageResponse{Data: dtos.NewClientResponses(clients), Total: total, Active: active, Inactive: inactive, Page: query.Page, PageSize: query.PageSize, TotalPages: int((total + int64(query.PageSize) - 1) / int64(query.PageSize))}
	return response, nil
}

func (s *AdminUserService) GetUserClientsPage(userID uuid.UUID, query dtos.ClientsListQuery) (dtos.UserClientsPageResponse, error) {
	var user *models.User
	var clientsPage dtos.ClientsPageResponse
	group := new(errgroup.Group)
	group.Go(func() error {
		loaded, err := s.userRepo.GetUserByID(userID)
		user = loaded
		return err
	})
	group.Go(func() error {
		loaded, err := s.ListUserClientsPage(userID, query)
		clientsPage = loaded
		return err
	})
	if err := group.Wait(); err != nil {
		return dtos.UserClientsPageResponse{}, err
	}
	return dtos.UserClientsPageResponse{ClientsPageResponse: clientsPage, User: dtos.NewAdminUserResponse(user)}, nil
}

// SetUserActive refuses to disable a primary root (ErrProtectedRootAccount):
// level 1 cannot be granted through the API, so disabling one could leave the
// platform without a primary root. Re-enabling is always allowed.
func (s *AdminUserService) SetUserActive(userID uuid.UUID, active bool) (*models.User, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado")
	}
	if !active && user.IsPrimaryRoot() {
		return nil, fmt.Errorf("%w: a primary root cannot be deactivated", ErrProtectedRootAccount)
	}
	if err := s.authRepo.SetUserEnabled(user.CognitoSub, active, "cognito"); err != nil {
		return nil, fmt.Errorf("error actualizando estado en cognito: %w", err)
	}
	user.IsActive = active
	if err := s.userRepo.UpdateUser(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *AdminUserService) UpdateUserInformation(userID uuid.UUID, firstName, lastName string) (*models.User, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado")
	}
	if firstName == "" || lastName == "" {
		return nil, fmt.Errorf("nombre y apellido son requeridos")
	}
	attrs := map[string]string{
		"given_name":  firstName,
		"family_name": lastName,
	}
	if err := s.authRepo.UpdateUser(user.CognitoSub, attrs, "cognito"); err != nil {
		return nil, fmt.Errorf("error actualizando cognito: %w", err)
	}
	user.FirstName = firstName
	user.LastName = lastName
	if err := s.userRepo.UpdateUser(user); err != nil {
		return nil, fmt.Errorf("error actualizando base de datos local: %w", err)
	}
	return user, nil
}

// guardDestructiveTarget rejects acting on a primary root (root_level 1, or a
// legacy is_root account without an explicit level) and on yourself. The
// primary-root check runs first so a primary root acting on itself gets the
// #285 contract (409 "Protected account"); the self check covers any other
// actor the route may admit.
func guardDestructiveTarget(actorID uuid.UUID, target *models.User) error {
	if target.IsPrimaryRoot() {
		return ErrAdminPrimaryRootProtected
	}
	if target.ID == actorID {
		return ErrAdminCannotTargetSelf
	}
	return nil
}

// DeactivateUser disables a user account on behalf of actorID after the
// self / primary-root guards.
func (s *AdminUserService) DeactivateUser(actorID, userID uuid.UUID) (*models.User, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return nil, ErrAdminUserNotFound
	}
	if err := guardDestructiveTarget(actorID, user); err != nil {
		return nil, err
	}
	return s.SetUserActive(userID, false)
}

// DeleteUser removes a user account on behalf of actorID.
//
// Guards: not yourself, not a primary root, and no active organization
// membership left (the admin must remove the memberships first).
//
// Order (chosen so a failure never leaves a local user without a usable
// Cognito identity, nor a Cognito identity that can sign in without a local
// user):
//  1. Disable the Cognito user (reversible). On failure nothing changed.
//  2. Delete the local user and its relations in one transaction. On failure
//     re-enable Cognito if the user was active before, and return the error.
//  3. Delete the Cognito user. On failure the local user is already gone and
//     the Cognito identity stays disabled (it cannot sign in); this is logged
//     for manual cleanup and the request still succeeds.
func (s *AdminUserService) DeleteUser(actorID, userID uuid.UUID) error {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return ErrAdminUserNotFound
	}
	if err := guardDestructiveTarget(actorID, user); err != nil {
		return err
	}
	if s.clientRepo == nil {
		return fmt.Errorf("no se pudo verificar las membresías del usuario")
	}
	memberships, err := s.clientRepo.ListClientsByUser(userID)
	if err != nil {
		return fmt.Errorf("no se pudo verificar las membresías del usuario: %w", err)
	}
	if len(memberships) > 0 {
		return ErrAdminUserHasActiveMembership
	}

	if err := s.authRepo.SetUserEnabled(user.CognitoSub, false, "cognito"); err != nil {
		return fmt.Errorf("error deshabilitando usuario en cognito: %w", err)
	}
	if err := s.userRepo.DeleteUser(userID); err != nil {
		if user.IsActive {
			if restoreErr := s.authRepo.SetUserEnabled(user.CognitoSub, true, "cognito"); restoreErr != nil {
				slog.Error("admin user delete: could not re-enable cognito user after local delete failed", "user_id", userID, "error", restoreErr)
			}
		}
		return fmt.Errorf("error eliminando usuario local: %w", err)
	}
	if err := s.authRepo.DeleteUser(user.CognitoSub, "cognito"); err != nil {
		slog.Error("admin user delete: local user deleted but cognito user could not be deleted; it remains disabled and needs manual cleanup", "user_id", userID, "error", err)
	}
	return nil
}

func (s *AdminUserService) InviteUser(email, firstName, lastName string) (*models.User, error) {
	return s.InviteUserForTenant(email, firstName, lastName, "eventiapp")
}

func (s *AdminUserService) InviteUserForTenant(email, firstName, lastName, tenantCode string) (*models.User, error) {
	email, firstName, lastName, err := normalizeInviteIdentity(email, firstName, lastName)
	if err != nil {
		return nil, err
	}
	var authUser *dtos.AuthUser
	if tenantRepo, ok := s.authRepo.(tenantInvitationAuthProvider); ok {
		authUser, err = tenantRepo.InviteUserForTenant(email, firstName, lastName, tenantCode, "cognito")
	} else {
		authUser, err = s.authRepo.InviteUser(email, firstName, lastName, "cognito")
	}
	if err != nil {
		return nil, err
	}
	user := &models.User{
		CognitoSub: authUser.Sub,
		Email:      authUser.Email,
		FirstName:  authUser.FirstName,
		LastName:   authUser.LastName,
		IsActive:   true,
		IsRoot:     false,
	}
	if err := s.userRepo.CreateUser(user); err != nil {
		_ = s.authRepo.DeleteUser(authUser.Sub, "cognito")
		return nil, err
	}
	return user, nil
}
