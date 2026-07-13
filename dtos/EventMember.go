package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type EventMemberResponse struct {
	EventID   uuid.UUID `json:"event_id"`
	UserID    uuid.UUID `json:"user_id"`
	Role      string    `json:"role"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func NewEventMemberResponse(member models.EventMember) EventMemberResponse {
	return EventMemberResponse{EventID: member.EventID, UserID: member.UserID, Role: member.Role, FirstName: member.User.FirstName, LastName: member.User.LastName, Email: member.User.Email, CreatedAt: member.CreatedAt}
}

func NewEventMemberResponses(members []models.EventMember) []EventMemberResponse {
	result := make([]EventMemberResponse, 0, len(members))
	for _, member := range members {
		result = append(result, NewEventMemberResponse(member))
	}
	return result
}
