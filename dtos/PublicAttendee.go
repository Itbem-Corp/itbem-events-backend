package dtos

import (
	"events-stocks/models"
	"time"
)

// PublicAttendee is the profile data shown by public attendee/graduation lists.
type PublicAttendee struct {
	FirstName             string     `json:"first_name"`
	LastName              string     `json:"last_name"`
	Nickname              string     `json:"nickname,omitempty"`
	Role                  string     `json:"role,omitempty"`
	Order                 int        `json:"order"`
	ImageURL              string     `json:"image_url,omitempty"`
	ImageViewURL          string     `json:"image_view_url,omitempty"`
	ImageViewURLExpiresAt *time.Time `json:"image_view_url_expires_at,omitempty"`
	Headline              string     `json:"headline,omitempty"`
	Bio                   string     `json:"bio,omitempty"`
	Signature             string     `json:"signature,omitempty"`
}

func NewPublicAttendee(guest models.Guest) PublicAttendee {
	return PublicAttendee{
		FirstName:    guest.FirstName,
		LastName:     guest.LastName,
		Nickname:     guest.Nickname,
		Role:         guest.Role,
		Order:        guest.Order,
		ImageURL:     guest.ImageURL,
		ImageViewURL: guest.ImageURL,
		Headline:     guest.Headline,
		Bio:          guest.Bio,
		Signature:    guest.Signature,
	}
}

func NewPublicAttendees(guests []models.Guest) []PublicAttendee {
	attendees := make([]PublicAttendee, 0, len(guests))
	for _, guest := range guests {
		attendees = append(attendees, NewPublicAttendee(guest))
	}
	return attendees
}
