package dtos

import (
	"time"

	"github.com/gofrs/uuid"
)

// Viewer access markers on event list rows (EventResponse.ViewerAccess).
const (
	EventViewerAccessOrganization = "organization"
	EventViewerAccessEventClient  = "event_client"
)

// EventClientCreateRequest is the body of POST /api/events/:id/clients.
// Exactly one form is valid: {email, first_name, last_name} or {user_id}.
type EventClientCreateRequest struct {
	Email     *string `json:"email"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	UserID    *string `json:"user_id"`
}

// EventClientAssignmentsRequest is the body of
// PATCH /api/events/:id/clients/:user_id/assignments.
type EventClientAssignmentsRequest struct {
	Assign   []string `json:"assign"`
	Unassign []string `json:"unassign"`
}

type EventClientResponse struct {
	UserID          uuid.UUID `json:"user_id"`
	Email           string    `json:"email"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	GrantedAt       time.Time `json:"granted_at"`
	GrantedByUserID uuid.UUID `json:"granted_by_user_id"`
}

type EventClientCreateResponse struct {
	Client          EventClientResponse `json:"client"`
	Account         string              `json:"account"`
	AlreadyAssigned bool                `json:"already_assigned"`
}

type EventClientCandidateResponse struct {
	UserID          uuid.UUID `json:"user_id"`
	Email           string    `json:"email"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	AssignedEvents  int       `json:"assigned_events"`
	AssignedToEvent bool      `json:"assigned_to_event"`
}

type EventClientAssignmentResponse struct {
	EventID       uuid.UUID `json:"event_id"`
	Name          string    `json:"name"`
	EventDateTime time.Time `json:"event_date_time"`
	Timezone      string    `json:"timezone"`
	IsActive      bool      `json:"is_active"`
	Assigned      bool      `json:"assigned"`
	CanManage     bool      `json:"can_manage"`
}

type EventClientAssignmentsUpdateResponse struct {
	Assigned       []uuid.UUID `json:"assigned"`
	Unassigned     []uuid.UUID `json:"unassigned"`
	AssignedEvents int         `json:"assigned_events"`
}

type EventClientPerson struct {
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
}

type OrganizationEventClientGrantResponse struct {
	GrantID       uuid.UUID         `json:"grant_id"`
	EventID       uuid.UUID         `json:"event_id"`
	EventName     string            `json:"event_name"`
	EventDateTime time.Time         `json:"event_date_time"`
	Client        EventClientPerson `json:"client"`
	GrantedBy     EventClientPerson `json:"granted_by"`
	GrantedAt     time.Time         `json:"granted_at"`
}

// EventClientPage is the paged envelope used by assignments and oversight.
type EventClientPage[T any] struct {
	Data       []T `json:"data"`
	Total      int `json:"total"`
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	TotalPages int `json:"total_pages"`
}

func NewEventClientPage[T any](data []T, total, page, pageSize int) EventClientPage[T] {
	if data == nil {
		data = []T{}
	}
	totalPages := 0
	if pageSize > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return EventClientPage[T]{Data: data, Total: total, Page: page, PageSize: pageSize, TotalPages: totalPages}
}
