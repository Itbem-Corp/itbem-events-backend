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
	AccessVersion     string     `json:"accessVersion,omitempty"`
	PreviewAuthorized bool       `json:"previewAuthorized,omitempty"`
	PasswordVerified  bool       `json:"passwordVerified,omitempty"`
}

// PageSpecTheme carries the effective visual theme selected in EventConfig.
// Direct EventConfig overrides win over the selected DesignTemplate defaults.
type PageSpecTheme struct {
	DesignTemplateID         string            `json:"designTemplateId,omitempty"`
	DesignTemplateIdentifier string            `json:"designTemplateIdentifier,omitempty"`
	ColorPaletteID           string            `json:"colorPaletteId,omitempty"`
	ColorPaletteName         string            `json:"colorPaletteName,omitempty"`
	FontSetID                string            `json:"fontSetId,omitempty"`
	FontSetName              string            `json:"fontSetName,omitempty"`
	Colors                   map[string]string `json:"colors,omitempty"`
	Fonts                    map[string]string `json:"fonts,omitempty"`
	FontURLs                 map[string]string `json:"fontUrls,omitempty"`
	FontViewURLs             map[string]string `json:"fontViewUrls,omitempty"`
	FontViewURLsExpiresAt    *time.Time        `json:"fontViewUrlsExpiresAt,omitempty"`
}

// PageSpecMeta holds event-level metadata for the SDUI page spec.
type PageSpecMeta struct {
	PageTitle              string           `json:"pageTitle"`
	MusicUrl               *string          `json:"musicUrl,omitempty"`
	Contact                *PageSpecContact `json:"contact,omitempty"`
	EventID                string           `json:"eventId,omitempty"`
	Identifier             string           `json:"identifier,omitempty"`
	CoverImageURL          string           `json:"coverImageUrl,omitempty"`
	CoverImageURLExpiresAt *time.Time       `json:"coverImageUrlExpiresAt,omitempty"`
	CoverViewURL           string           `json:"coverViewUrl,omitempty"`
	CoverViewURLExpiresAt  *time.Time       `json:"coverViewUrlExpiresAt,omitempty"`
	EventDateTime          *time.Time       `json:"eventDateTime,omitempty"`
	Address                string           `json:"address,omitempty"`
	SecondAddress          string           `json:"secondAddress,omitempty"`
	Timezone               string           `json:"timezone,omitempty"`
	Language               string           `json:"language,omitempty"`
	EventType              string           `json:"eventType,omitempty"`
	ContentVersion         string           `json:"contentVersion,omitempty"`
	Access                 *PageSpecAccess  `json:"access,omitempty"`
	FooterVisible          bool             `json:"footerVisible"`
	Theme                  *PageSpecTheme   `json:"theme,omitempty"`
}

// PageSpecSection describes one section of the page in SDUI format.
type PageSpecSection struct {
	Type      string          `json:"type"` // e.g. "GraduationHero", "CountdownHeader"
	Title     string          `json:"title,omitempty"`
	SectionId string          `json:"sectionId"` // EventSection UUID (for resources API)
	Order     int             `json:"order"`
	Config    json.RawMessage `json:"config"` // Section-specific config object
}

// PageSpec is the full SDUI spec returned by GET /api/events/page-spec?token=...
type PageSpec struct {
	Meta     PageSpecMeta      `json:"meta"`
	Sections []PageSpecSection `json:"sections"`
}
