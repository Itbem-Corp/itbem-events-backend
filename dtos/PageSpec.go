package dtos

import (
	"encoding/json"
	"time"
)

// PageSpecContact carries organizer contact info for the public event footer.
// Fields are omitempty - the object is absent from JSON when the event has no contact data.
type PageSpecContact struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// PageSpecAccess carries event access-control settings for the public page.
// The frontend uses these to show "Coming Soon", "Event Ended", or a password gate.
// The actual password is NEVER sent to the client — only a boolean flag.
type PageSpecAccess struct {
	ActiveFrom        *time.Time `json:"activeFrom,omitempty"`
	ActiveUntil       *time.Time `json:"activeUntil,omitempty"`
	PasswordProtected bool       `json:"passwordProtected"`
}

// PageSpecMeta holds event-level metadata for the SDUI page spec.
type PageSpecMeta struct {
	PageTitle  string           `json:"pageTitle"`
	MusicUrl   *string          `json:"musicUrl,omitempty"`
	Contact    *PageSpecContact `json:"contact,omitempty"`
	EventID    string           `json:"eventId,omitempty"`
	Identifier string           `json:"identifier,omitempty"`
	Access     *PageSpecAccess  `json:"access,omitempty"`
}

// PageSpecSection describes one section of the page in SDUI format.
type PageSpecSection struct {
	Type      string          `json:"type"`      // e.g. "GraduationHero", "CountdownHeader"
	SectionId string          `json:"sectionId"` // EventSection UUID (for resources API)
	Order     int             `json:"order"`
	Config    json.RawMessage `json:"config"` // Section-specific config object
}

// PageSpec is the full SDUI spec returned by GET /api/events/page-spec?token=...
type PageSpec struct {
	Meta     PageSpecMeta      `json:"meta"`
	Sections []PageSpecSection `json:"sections"`
}
