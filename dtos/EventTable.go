package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type EventTableResponse struct {
	ID        uuid.UUID `json:"id"`
	EventID   uuid.UUID `json:"event_id"`
	Name      string    `json:"name"`
	Capacity  int       `json:"capacity"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SeatingPlanCreateTable struct {
	TempID    string `json:"temp_id"`
	Name      string `json:"name"`
	Capacity  int    `json:"capacity"`
	SortOrder int    `json:"sort_order"`
}

type SeatingPlanUpdateTable struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Capacity  int    `json:"capacity"`
	SortOrder int    `json:"sort_order"`
}

type SeatingPlanAssignment struct {
	GuestID string  `json:"guest_id"`
	TableID *string `json:"table_id"`
}

type SeatingPlanSaveRequest struct {
	Created     []SeatingPlanCreateTable `json:"created"`
	Updated     []SeatingPlanUpdateTable `json:"updated"`
	DeletedIDs  []string                 `json:"deleted_ids"`
	Assignments []SeatingPlanAssignment  `json:"assignments"`
}

func NewEventTableResponse(table models.EventTable) EventTableResponse {
	return EventTableResponse{
		ID:        table.ID,
		EventID:   table.EventID,
		Name:      table.Name,
		Capacity:  table.Capacity,
		SortOrder: table.SortOrder,
		CreatedAt: table.CreatedAt,
		UpdatedAt: table.UpdatedAt,
	}
}

func NewEventTableResponses(tables []models.EventTable) []EventTableResponse {
	response := make([]EventTableResponse, 0, len(tables))
	for _, table := range tables {
		response = append(response, NewEventTableResponse(table))
	}
	return response
}
