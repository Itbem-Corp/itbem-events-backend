package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type EventTypeResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewEventTypeResponse(eventType models.EventType) EventTypeResponse {
	return EventTypeResponse{
		ID:        eventType.ID,
		Name:      eventType.Name,
		CreatedAt: eventType.CreatedAt,
		UpdatedAt: eventType.UpdatedAt,
	}
}

func NewEventTypeResponses(eventTypes []models.EventType) []EventTypeResponse {
	response := make([]EventTypeResponse, 0, len(eventTypes))
	for _, eventType := range eventTypes {
		response = append(response, NewEventTypeResponse(eventType))
	}
	return response
}
