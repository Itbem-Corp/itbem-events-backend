package dtos

import "time"

// EventMeta is the public-safe metadata used by OG tags and shared links.
type EventMeta struct {
	Name                  string     `json:"name"`
	Identifier            string     `json:"identifier,omitempty"`
	Description           string     `json:"description,omitempty"`
	CoverImageURL         string     `json:"cover_image_url,omitempty"`
	CoverViewURL          string     `json:"cover_view_url,omitempty"`
	CoverViewURLExpiresAt *time.Time `json:"cover_view_url_expires_at,omitempty"`
	ViewURL               string     `json:"view_url,omitempty"`
	ViewURLExpiresAt      *time.Time `json:"view_url_expires_at,omitempty"`
	EventDateTime         *time.Time `json:"event_date_time,omitempty"`
	Address               string     `json:"address,omitempty"`
	SecondAddress         string     `json:"second_address,omitempty"`
	Timezone              string     `json:"timezone,omitempty"`
	Language              string     `json:"language,omitempty"`
	OrganizerName         string     `json:"organizer_name,omitempty"`
	EventType             string     `json:"event_type,omitempty"`
	ContentVersion        string     `json:"content_version,omitempty"`
}

type PreviewTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type EventCoverResponse struct {
	CoverImageURL         string     `json:"cover_image_url"`
	CoverViewURL          string     `json:"cover_view_url,omitempty"`
	CoverViewURLExpiresAt *time.Time `json:"cover_view_url_expires_at,omitempty"`
	ViewURL               string     `json:"view_url"`
	ViewURLExpiresAt      *time.Time `json:"view_url_expires_at,omitempty"`
}

type EventPhrasesResponse struct {
	Phrases []string `json:"phrases"`
}

type EventRepairResponse struct {
	Repaired bool     `json:"repaired"`
	Fixes    []string `json:"fixes"`
	Warnings []string `json:"warnings"`
}
