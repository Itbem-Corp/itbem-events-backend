package dtos

// GuestSummary is the compact event-level RSVP rollup consumed by the
// dashboard. Counts refer to invitations/guest records; TotalAttendees is the
// confirmed party-size sum.
type GuestSummary struct {
	Total          int64 `json:"total" gorm:"column:total"`
	Confirmed      int64 `json:"confirmed" gorm:"column:confirmed"`
	Pending        int64 `json:"pending" gorm:"column:pending"`
	Declined       int64 `json:"declined" gorm:"column:declined"`
	TotalAttendees int64 `json:"total_attendees" gorm:"column:total_attendees"`
}
