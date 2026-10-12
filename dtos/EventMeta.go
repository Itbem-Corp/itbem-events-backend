package dtos

import (
	"time"

	"github.com/gofrs/uuid"
)

// PublicEventResponse is the anonymous read of GET /api/events/:key. It keeps
// the EventResponse JSON names of the fields it shares so older readers keep
// working, and carries nothing about the organization, organizer contact,
// capacity, configuration, guests or media processing.
type PublicEventResponse struct {
	ID                    uuid.UUID  `json:"id"`
	Name                  string     `json:"name"`
	Identifier            string     `json:"identifier"`
	Description           string     `json:"description"`
	CoverImageURL         string     `json:"cover_image_url"`
	CoverViewURL          string     `json:"cover_view_url,omitempty"`
	CoverViewURLExpiresAt *time.Time `json:"cover_view_url_expires_at,omitempty"`
	ViewURL               string     `json:"view_url,omitempty"`
	ViewURLExpiresAt      *time.Time `json:"view_url_expires_at,omitempty"`
	EventDateTime         time.Time  `json:"event_date_time"`
	Address               string     `json:"address"`
	SecondAddress         string     `json:"second_address"`
	Timezone              string     `json:"timezone"`
	Language              string     `json:"language"`
	OrganizerName         string     `json:"organizer_name"`
}

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
	// OGImageURL is a stable path (relative to the API origin) that redirects
	// to a fresh cover URL; use it for og:image instead of the expiring
	// cover_view_url (STU-49). Empty when the event has no cover.
	OGImageURL     string     `json:"og_image_url,omitempty"`
	EventDateTime  *time.Time `json:"event_date_time,omitempty"`
	Address        string     `json:"address,omitempty"`
	SecondAddress  string     `json:"second_address,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	Language       string     `json:"language,omitempty"`
	OrganizerName  string     `json:"organizer_name,omitempty"`
	EventType      string     `json:"event_type,omitempty"`
	ContentVersion string     `json:"content_version,omitempty"`
	RSVPDeadline   *time.Time `json:"rsvp_deadline"`
	RSVPOpen       bool       `json:"rsvp_open"`
}

type PreviewTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type EventCoverResponse struct {
	CoverImageURL           string               `json:"cover_image_url"`
	CoverViewURL            string               `json:"cover_view_url,omitempty"`
	CoverViewURLExpiresAt   *time.Time           `json:"cover_view_url_expires_at,omitempty"`
	ViewURL                 string               `json:"view_url"`
	PendingURL              string               `json:"pending_url,omitempty"`
	PendingViewURL          string               `json:"pending_view_url,omitempty"`
	PendingViewURLExpiresAt *time.Time           `json:"pending_view_url_expires_at,omitempty"`
	ProcessingStatus        string               `json:"processing_status,omitempty"`
	ProcessingJobID         string               `json:"processing_job_id,omitempty"`
	ProcessingGeneration    int64                `json:"processing_generation,omitempty"`
	ProcessingError         string               `json:"processing_error,omitempty"`
	ViewURLExpiresAt        *time.Time           `json:"view_url_expires_at,omitempty"`
	Variants                []PublicMediaVariant `json:"variants,omitempty"`
}

type EventPhrasesResponse struct {
	Phrases []string `json:"phrases"`
}

type EventRepairResponse struct {
	Repaired bool     `json:"repaired"`
	Fixes    []string `json:"fixes"`
	Warnings []string `json:"warnings"`
}

// PublicErrorResponse is the error envelope for public EventiApp endpoints that
// need a machine-readable reason the public site can branch on. It keeps the
// standard {status, message, error} keys and adds code; build it with
// NewPublicError so the code is always in both "code" and "error".
type PublicErrorResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Error   string `json:"error,omitempty"`
}

// NewPublicError builds a PublicErrorResponse with the stable lower_snake code
// in both "code" and "error" (the convention of every public error).
func NewPublicError(status int, message, code string) PublicErrorResponse {
	return PublicErrorResponse{Status: status, Message: message, Code: code, Error: code}
}
