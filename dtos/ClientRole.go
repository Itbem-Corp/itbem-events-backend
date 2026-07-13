package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type ClientRoleResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Description string    `json:"description"`
	Hierarchy   int       `json:"hierarchy"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewClientRoleResponse(role models.ClientRole) ClientRoleResponse {
	return ClientRoleResponse{
		ID:          role.ID,
		Name:        role.Name,
		Code:        role.Code,
		Description: role.Description,
		Hierarchy:   role.Hierarchy,
		IsActive:    role.IsActive,
		CreatedAt:   role.CreatedAt,
		UpdatedAt:   role.UpdatedAt,
	}
}

func NewClientRoleResponses(roles []models.ClientRole) []ClientRoleResponse {
	response := make([]ClientRoleResponse, 0, len(roles))
	for _, role := range roles {
		response = append(response, NewClientRoleResponse(role))
	}
	return response
}
