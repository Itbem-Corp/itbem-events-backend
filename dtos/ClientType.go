package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type ClientTypeResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Description string    `json:"description"`
	Level       int       `json:"level"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewClientTypeResponse(clientType models.ClientType) ClientTypeResponse {
	return ClientTypeResponse{
		ID:          clientType.ID,
		Name:        clientType.Name,
		Code:        clientType.Code,
		Description: clientType.Description,
		Level:       clientType.Level,
		IsActive:    clientType.IsActive,
		CreatedAt:   clientType.CreatedAt,
		UpdatedAt:   clientType.UpdatedAt,
	}
}

func NewClientTypeResponses(clientTypes []models.ClientType) []ClientTypeResponse {
	response := make([]ClientTypeResponse, 0, len(clientTypes))
	for _, clientType := range clientTypes {
		response = append(response, NewClientTypeResponse(clientType))
	}
	return response
}
