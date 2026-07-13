package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type ResourceResponse struct {
	ID               uuid.UUID  `json:"id"`
	EventSectionID   uuid.UUID  `json:"event_section_id"`
	ResourceTypeID   uuid.UUID  `json:"resource_type_id"`
	AltText          string     `json:"alt_text"`
	Title            string     `json:"title"`
	Position         int        `json:"position"`
	URL              string     `json:"url,omitempty"`
	ViewURL          string     `json:"view_url"`
	ViewURLExpiresAt *time.Time `json:"view_url_expires_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
}

type ResourceFileMutationResponse struct {
	Path             string     `json:"path"`
	URL              string     `json:"url,omitempty"`
	ViewURL          string     `json:"view_url"`
	ViewURLExpiresAt *time.Time `json:"view_url_expires_at,omitempty"`
}

type AdminResourceResponse struct {
	ResourceResponse
	Path      string    `json:"path"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewResourceResponse(resource *models.Resource, viewURL string, viewURLExpiresAt *time.Time) ResourceResponse {
	if resource == nil {
		return ResourceResponse{URL: viewURL, ViewURL: viewURL, ViewURLExpiresAt: viewURLExpiresAt}
	}

	var sectionID uuid.UUID
	if resource.EventSectionID != nil {
		sectionID = *resource.EventSectionID
	}

	var position int
	if resource.Position != nil {
		position = *resource.Position
	}

	return ResourceResponse{
		ID:               resource.ID,
		EventSectionID:   sectionID,
		ResourceTypeID:   resource.ResourceTypeID,
		AltText:          resource.AltText,
		Title:            resource.Title,
		Position:         position,
		URL:              viewURL,
		ViewURL:          viewURL,
		ViewURLExpiresAt: viewURLExpiresAt,
		CreatedAt:        resource.CreatedAt,
	}
}

func NewAdminResourceResponse(resource *models.Resource, viewURL string, viewURLExpiresAt *time.Time) AdminResourceResponse {
	if resource == nil {
		return AdminResourceResponse{ResourceResponse: NewResourceResponse(nil, viewURL, viewURLExpiresAt)}
	}

	return AdminResourceResponse{
		ResourceResponse: NewResourceResponse(resource, viewURL, viewURLExpiresAt),
		Path:             resource.Path,
		UpdatedAt:        resource.UpdatedAt,
	}
}

func NewResourceFileMutationResponse(path string, viewURL string, viewURLExpiresAt *time.Time) ResourceFileMutationResponse {
	return ResourceFileMutationResponse{
		Path:             path,
		URL:              viewURL,
		ViewURL:          viewURL,
		ViewURLExpiresAt: viewURLExpiresAt,
	}
}

func NewResourceResponses(resources []models.Resource, viewURLFor func(models.Resource) (string, *time.Time, bool)) []ResourceResponse {
	response := make([]ResourceResponse, 0, len(resources))
	for _, resource := range resources {
		viewURL, expiresAt, ok := viewURLFor(resource)
		if !ok {
			continue
		}
		response = append(response, NewResourceResponse(&resource, viewURL, expiresAt))
	}
	return response
}

func NewAdminResourceResponses(resources []models.Resource, viewURLFor func(models.Resource) (string, *time.Time, bool)) []AdminResourceResponse {
	response := make([]AdminResourceResponse, 0, len(resources))
	for _, resource := range resources {
		viewURL, expiresAt, ok := viewURLFor(resource)
		if !ok {
			continue
		}
		response = append(response, NewAdminResourceResponse(&resource, viewURL, expiresAt))
	}
	return response
}
