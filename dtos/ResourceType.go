package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type ResourceTypeResponse struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewResourceTypeResponse(resourceType models.ResourceType) ResourceTypeResponse {
	return ResourceTypeResponse{
		ID:        resourceType.ID,
		Code:      resourceType.Code,
		Label:     resourceType.Label,
		CreatedAt: resourceType.CreatedAt,
		UpdatedAt: resourceType.UpdatedAt,
	}
}

func NewResourceTypeResponses(resourceTypes []models.ResourceType) []ResourceTypeResponse {
	response := make([]ResourceTypeResponse, 0, len(resourceTypes))
	for _, resourceType := range resourceTypes {
		response = append(response, NewResourceTypeResponse(resourceType))
	}
	return response
}
