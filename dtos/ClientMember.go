package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type ClientMemberUserResponse struct {
	ID           uuid.UUID `json:"id"`
	FirstName    string    `json:"first_name"`
	LastName     string    `json:"last_name"`
	Email        string    `json:"email"`
	ProfileImage string    `json:"profile_image"`
	IsActive     bool      `json:"is_active"`
}

type ClientMemberResponse struct {
	ID           uuid.UUID                `json:"id"`
	ClientID     uuid.UUID                `json:"client_id"`
	UserID       uuid.UUID                `json:"user_id"`
	FirstName    string                   `json:"first_name"`
	LastName     string                   `json:"last_name"`
	Email        string                   `json:"email"`
	ProfileImage string                   `json:"profile_image"`
	RoleID       uuid.UUID                `json:"role_id"`
	RoleCode     string                   `json:"role_code"`
	Role         string                   `json:"role"`
	RoleName     string                   `json:"role_name"`
	JoinedAt     time.Time                `json:"joined_at"`
	User         ClientMemberUserResponse `json:"user"`
}

type ClientMemberLinkResponse struct {
	UserID   uuid.UUID `json:"user_id"`
	ClientID uuid.UUID `json:"client_id"`
	RoleID   uuid.UUID `json:"role_id"`
	Email    string    `json:"email,omitempty"`
}

type ClientMembersPage struct {
	Data       []ClientMemberResponse `json:"data"`
	Total      int64                  `json:"total"`
	Page       int                    `json:"page"`
	PageSize   int                    `json:"page_size"`
	TotalPages int                    `json:"total_pages"`
}

func NewClientMemberResponse(member models.ClientMember) ClientMemberResponse {
	user := ClientMemberUserResponse{
		ID:           member.UserID,
		FirstName:    member.User.FirstName,
		LastName:     member.User.LastName,
		Email:        member.User.Email,
		ProfileImage: member.User.ProfileImage,
		IsActive:     member.User.IsActive,
	}

	return ClientMemberResponse{
		ID:           member.ID,
		ClientID:     member.ClientID,
		UserID:       member.UserID,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		Email:        user.Email,
		ProfileImage: user.ProfileImage,
		RoleID:       member.ClientRoleID,
		RoleCode:     member.ClientRole.Code,
		Role:         member.ClientRole.Code,
		RoleName:     member.ClientRole.Name,
		JoinedAt:     member.CreatedAt,
		User:         user,
	}
}

func NewClientMemberResponses(members []models.ClientMember) []ClientMemberResponse {
	response := make([]ClientMemberResponse, 0, len(members))
	for _, member := range members {
		response = append(response, NewClientMemberResponse(member))
	}
	return response
}
