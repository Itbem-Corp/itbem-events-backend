package dtos

import "github.com/gofrs/uuid"

type GuestShareRecipient struct {
	ID          uuid.UUID `json:"id"`
	FirstName   string    `json:"first_name"`
	Email       string    `json:"email"`
	PrettyToken string    `json:"pretty_token"`
}

type GuestShareSummary struct {
	Total            int64                `json:"total"`
	WithEmail        int64                `json:"with_email"`
	WithPhone        int64                `json:"with_phone"`
	PendingWithEmail int64                `json:"pending_with_email"`
	FirstPending     *GuestShareRecipient `json:"first_pending,omitempty" gorm:"-"`
}
