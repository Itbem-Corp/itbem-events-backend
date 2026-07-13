package users

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"fmt"
	"strings"

	"github.com/gofrs/uuid"
	"golang.org/x/sync/errgroup"
)

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

func (s *AdminUserService) SetUserActive(userID uuid.UUID, active bool) (*models.User, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado")
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

func (s *AdminUserService) DeleteUser(userID uuid.UUID) error {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("usuario no encontrado")
	}
	if err := s.authRepo.DeleteUser(user.CognitoSub, "cognito"); err != nil {
		return fmt.Errorf("error eliminando usuario en cognito: %w", err)
	}
	if err := s.userRepo.DeleteUser(userID); err != nil {
		return fmt.Errorf("error eliminando usuario local: %w", err)
	}
	return nil
}

func (s *AdminUserService) InviteUser(email, firstName, lastName string) (*models.User, error) {
	authUser, err := s.authRepo.InviteUser(email, firstName, lastName, "cognito")
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
