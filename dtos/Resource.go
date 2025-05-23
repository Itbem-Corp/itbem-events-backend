package dtos

import (
	"time"

	"github.com/gofrs/uuid"
)

type ResourceResponse struct {
	ID             uuid.UUID `json:"id"`
	EventSectionID uuid.UUID `json:"event_section_id"`
	ResourceTypeID uuid.UUID `json:"resource_type_id"`
	AltText        string    `json:"alt_text"`
	Title          string    `json:"title"`
	Position       int       `json:"position"`
	ViewURL        string    `json:"view_url"`
	CreatedAt      time.Time `json:"created_at"`
}
