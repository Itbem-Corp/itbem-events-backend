package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type GuestStatusResponse struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Label     string    `json:"label"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Order     int       `json:"order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewGuestStatusResponse(status models.GuestStatus) GuestStatusResponse {
	return GuestStatusResponse{
		ID:        status.ID,
		Code:      status.Code,
		Label:     status.Label,
		Name:      status.Label,
		Color:     status.Color,
		Order:     status.Order,
		CreatedAt: status.CreatedAt,
		UpdatedAt: status.UpdatedAt,
	}
}

func guestStatusResponsePtr(status models.GuestStatus) *GuestStatusResponse {
	if status.ID == uuid.Nil {
		return nil
	}
	response := NewGuestStatusResponse(status)
	return &response
}

func NewGuestStatusResponses(statuses []models.GuestStatus) []GuestStatusResponse {
	response := make([]GuestStatusResponse, 0, len(statuses))
	for _, status := range statuses {
		response = append(response, NewGuestStatusResponse(status))
	}
	return response
}
