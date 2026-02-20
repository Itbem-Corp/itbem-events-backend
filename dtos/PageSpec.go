package dtos

import "encoding/json"

// PageSpecMeta holds event-level metadata for the SDUI page spec.
type PageSpecMeta struct {
	PageTitle string  `json:"pageTitle"`
	MusicUrl  *string `json:"musicUrl,omitempty"`
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
