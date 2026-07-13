package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type UserProfileResponse struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	FirstName    string    `json:"first_name"`
	LastName     string    `json:"last_name"`
	ProfileImage string    `json:"profile_image"`
	IsActive     bool      `json:"is_active"`
	IsRoot       bool      `json:"is_root"`
	RootLevel    int       `json:"root_level"`
}

type AvatarResponse struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

type AdminUserResponse struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	IsActive  bool      `json:"is_active"`
	IsRoot    bool      `json:"is_root"`
	RootLevel int       `json:"root_level"`
	CreatedAt time.Time `json:"created_at"`
}

type AdminUserListItemResponse struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	IsActive  bool      `json:"is_active"`
	IsRoot    bool      `json:"is_root"`
	RootLevel int       `json:"root_level"`
	Clients   int64     `json:"clients"`
	CreatedAt time.Time `json:"created_at"`
}

type AdminUsersPageResponse struct {
	Data       []AdminUserListItemResponse `json:"data"`
	Total      int                         `json:"total"`
	Page       int                         `json:"page"`
	PageSize   int                         `json:"page_size"`
	TotalPages int                         `json:"total_pages"`
}

type AdminUsersListQuery struct {
	Page     int
	PageSize int
	Search   string
	Status   string
}

type AdminUserDetailResponse struct {
	ID        uuid.UUID        `json:"id"`
	Email     string           `json:"email"`
	FirstName string           `json:"first_name"`
	LastName  string           `json:"last_name"`
	IsActive  bool             `json:"is_active"`
	IsRoot    bool             `json:"is_root"`
	RootLevel int              `json:"root_level"`
	Clients   []ClientResponse `json:"clients"`
	CreatedAt time.Time        `json:"created_at"`
}

type UserClientsPageResponse struct {
	ClientsPageResponse
	User AdminUserResponse `json:"user"`
}

func NewUserProfileResponse(user *models.User, profileImageURL string) UserProfileResponse {
	if user == nil {
		return UserProfileResponse{}
	}
	return UserProfileResponse{
		ID:           user.ID,
		Email:        user.Email,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		ProfileImage: profileImageURL,
		IsActive:     user.IsActive,
		IsRoot:       user.IsPlatformAdmin(),
		RootLevel:    user.EffectiveRootLevel(),
	}
}

func NewAdminUserResponse(user *models.User) AdminUserResponse {
	if user == nil {
		return AdminUserResponse{}
	}
	return AdminUserResponse{
		ID:        user.ID,
		Email:     user.Email,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		IsActive:  user.IsActive,
		IsRoot:    user.IsPlatformAdmin(),
		RootLevel: user.EffectiveRootLevel(),
		CreatedAt: user.CreatedAt,
	}
}

func NewAdminUserListItemResponse(user models.User, clientCount int64) AdminUserListItemResponse {
	return AdminUserListItemResponse{
		ID:        user.ID,
		Email:     user.Email,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		IsActive:  user.IsActive,
		IsRoot:    user.IsPlatformAdmin(),
		RootLevel: user.EffectiveRootLevel(),
		Clients:   clientCount,
		CreatedAt: user.CreatedAt,
	}
}

func NewAdminUsersPageResponse(users []models.User, clientCounts map[uuid.UUID]int64, total int64, page, pageSize int) AdminUsersPageResponse {
	totalPages := 0
	if total > 0 {
		totalPages = (int(total) + pageSize - 1) / pageSize
	}

	data := make([]AdminUserListItemResponse, 0, len(users))
	for _, user := range users {
		data = append(data, NewAdminUserListItemResponse(user, clientCounts[user.ID]))
	}

	return AdminUsersPageResponse{
		Data:       data,
		Total:      int(total),
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
}

func NewAdminUserDetailResponse(user *models.User, clients []models.Client) AdminUserDetailResponse {
	if user == nil {
		return AdminUserDetailResponse{}
	}
	return AdminUserDetailResponse{
		ID:        user.ID,
		Email:     user.Email,
		FirstName: user.FirstName,
		LastName:  user.LastName,
		IsActive:  user.IsActive,
		IsRoot:    user.IsPlatformAdmin(),
		RootLevel: user.EffectiveRootLevel(),
		Clients:   NewClientResponses(clients),
		CreatedAt: user.CreatedAt,
	}
}
