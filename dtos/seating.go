package dtos

type SeatAssignment struct {
	GuestID string  `json:"guest_id"`
	TableID *string `json:"table_id"` // nil = unassign
}

type BatchAssignRequest struct {
	Assignments []SeatAssignment `json:"assignments"`
}
