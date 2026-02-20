package users

import (
	"events-stocks/models"
	"events-stocks/repositories/authproviderrepository"
	"events-stocks/repositories/clientrepository"
	"events-stocks/repositories/userrepository"
	"events-stocks/services/ports"
	"fmt"
	"github.com/gofrs/uuid"
)

func ListAllUsers() (interface{}, error) {
	users, err := userrepository.ListAllUsers()
	if err != nil {
		return nil, err
	}

	userIDs := make([]uuid.UUID, len(users))
	for i, u := range users {
		userIDs[i] = u.ID
	}
	clientCounts, _ := clientrepository.CountClientsByUsers(userIDs)

	response := make([]map[string]interface{}, 0, len(users))
	for _, u := range users {
		response = append(response, map[string]interface{}{
			"id":         u.ID,
			"email":      u.Email,
			"first_name": u.FirstName,
			"last_name":  u.LastName,
			"is_active":  u.IsActive,
			"is_root":    u.IsRoot,
			"clients":    clientCounts[u.ID],
			"created_at": u.CreatedAt,
		})
	}

	return response, nil
}

func GetUserDetail(userID uuid.UUID) (interface{}, error) {
	user, err := userrepository.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado")
	}

	clients, _ := clientrepository.ListClientsByUser(userID)

	return map[string]interface{}{
		"id":         user.ID,
		"email":      user.Email,
		"first_name": user.FirstName,
		"last_name":  user.LastName,
		"is_active":  user.IsActive,
		"is_root":    user.IsRoot,
		"clients":    clients,
		"created_at": user.CreatedAt,
	}, nil
}

func ListUserClients(userID uuid.UUID) (interface{}, error) {
	clients, err := clientrepository.ListClientsByUser(userID)
	if err != nil {
		return nil, err
	}

	return clients, nil
}

func SetUserActive(userID uuid.UUID, active bool) error {
	user, err := userrepository.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("usuario no encontrado")
	}

	// 🔐 1. Cognito es la fuente de verdad
	if err := authproviderrepository.SetUserEnabled(
		user.CognitoSub,
		active,
		"cognito",
	); err != nil {
		return fmt.Errorf("error actualizando estado en cognito: %w", err)
	}

	// 🪞 2. DB es espejo
	user.IsActive = active
	return userrepository.UpdateUser(user)
}

func InviteUser(email, firstName, lastName string) (*models.User, error) {

	authUser, err := authproviderrepository.InviteUser(
		email,
		firstName,
		lastName,
		"cognito",
	)
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

	if err := userrepository.CreateUser(user); err != nil {
		_ = authproviderrepository.DeleteUser(authUser.Sub, "cognito")
		return nil, err
	}

	return user, nil
}

// PaginatedResult wraps a paginated list response with metadata.
type PaginatedResult struct {
	Data       interface{} `json:"data"`
	Total      int         `json:"total"`
	Page       int         `json:"page"`
	PageSize   int         `json:"page_size"`
	TotalPages int         `json:"total_pages"`
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

func (s *AdminUserService) ListAllUsers(page, pageSize int) (interface{}, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	if page < 1 {
		page = 1
	}

	paged, total, err := s.userRepo.ListAllUsersPaginated(page, pageSize)
	if err != nil {
		return nil, err
	}

	totalPages := 0
	if total > 0 {
		totalPages = (int(total) + pageSize - 1) / pageSize
	}

	userIDs := make([]uuid.UUID, len(paged))
	for i, u := range paged {
		userIDs[i] = u.ID
	}
	clientCounts, _ := s.clientRepo.CountClientsByUsers(userIDs)

	data := make([]map[string]interface{}, 0, len(paged))
	for _, u := range paged {
		data = append(data, map[string]interface{}{
			"id":         u.ID,
			"email":      u.Email,
			"first_name": u.FirstName,
			"last_name":  u.LastName,
			"is_active":  u.IsActive,
			"is_root":    u.IsRoot,
			"clients":    clientCounts[u.ID],
			"created_at": u.CreatedAt,
		})
	}

	return PaginatedResult{
		Data:       data,
		Total:      int(total),
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}, nil
}

func (s *AdminUserService) GetUserDetail(userID uuid.UUID) (interface{}, error) {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("usuario no encontrado")
	}
	clients, _ := s.clientRepo.ListClientsByUser(userID)
	return map[string]interface{}{
		"id":         user.ID,
		"email":      user.Email,
		"first_name": user.FirstName,
		"last_name":  user.LastName,
		"is_active":  user.IsActive,
		"is_root":    user.IsRoot,
		"clients":    clients,
		"created_at": user.CreatedAt,
	}, nil
}

func (s *AdminUserService) ListUserClients(userID uuid.UUID) (interface{}, error) {
	clients, err := s.clientRepo.ListClientsByUser(userID)
	if err != nil {
		return nil, err
	}
	return clients, nil
}

func (s *AdminUserService) SetUserActive(userID uuid.UUID, active bool) error {
	user, err := s.userRepo.GetUserByID(userID)
	if err != nil {
		return fmt.Errorf("usuario no encontrado")
	}
	if err := s.authRepo.SetUserEnabled(user.CognitoSub, active, "cognito"); err != nil {
		return fmt.Errorf("error actualizando estado en cognito: %w", err)
	}
	user.IsActive = active
	return s.userRepo.UpdateUser(user)
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
