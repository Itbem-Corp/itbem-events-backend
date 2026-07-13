package dtos

import (
	"events-stocks/models"
	"time"

	"github.com/gofrs/uuid"
)

type MomentResponse struct {
	ID                        uuid.UUID  `json:"id"`
	EventID                   *uuid.UUID `json:"event_id,omitempty"`
	InvitationID              *uuid.UUID `json:"invitation_id,omitempty"`
	MomentTypeID              *uuid.UUID `json:"moment_type_id,omitempty"`
	GuestID                   *uuid.UUID `json:"guest_id,omitempty"`
	Title                     string     `json:"title"`
	Description               string     `json:"description"`
	ContentURL                string     `json:"content_url"`
	ThumbnailURL              string     `json:"thumbnail_url,omitempty"`
	ContentURLExpiresAt       *time.Time `json:"content_url_expires_at,omitempty"`
	ThumbnailURLExpiresAt     *time.Time `json:"thumbnail_url_expires_at,omitempty"`
	ContentViewURL            string     `json:"content_view_url,omitempty"`
	ThumbnailViewURL          string     `json:"thumbnail_view_url,omitempty"`
	ContentViewURLExpiresAt   *time.Time `json:"content_view_url_expires_at,omitempty"`
	ThumbnailViewURLExpiresAt *time.Time `json:"thumbnail_view_url_expires_at,omitempty"`
	ContentType               string     `json:"content_type,omitempty"`
	IsApproved                bool       `json:"is_approved"`
	ProcessingStatus          string     `json:"processing_status,omitempty"`
	ProcessingDurationMs      int64      `json:"processing_duration_ms,omitempty"`
	OriginalSizeBytes         int64      `json:"original_size_bytes,omitempty"`
	OptimizedSizeBytes        int64      `json:"optimized_size_bytes,omitempty"`
	ErrorMessage              string     `json:"error_message,omitempty"`
	Order                     int        `json:"order,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

type MomentDashboardCounts struct {
	Total    int64 `json:"total"`
	Pending  int64 `json:"pending"`
	Approved int64 `json:"approved"`
	Failed   int64 `json:"failed"`
	Photos   int64 `json:"photos"`
	Videos   int64 `json:"videos"`
	Notes    int64 `json:"notes"`
	Legacy   int64 `json:"legacy"`
}

type MomentDashboardPage struct {
	Data         []MomentResponse      `json:"data"`
	InFlight     []MomentResponse      `json:"in_flight"`
	Reoptimizing []MomentResponse      `json:"reoptimizing"`
	Total        int64                 `json:"total"`
	Page         int                   `json:"page"`
	PageSize     int                   `json:"page_size"`
	TotalPages   int                   `json:"total_pages"`
	Counts       MomentDashboardCounts `json:"counts"`
}

func NewMomentResponse(moment *models.Moment) MomentResponse {
	if moment == nil {
		return MomentResponse{}
	}

	return MomentResponse{
		ID:                        moment.ID,
		EventID:                   moment.EventID,
		InvitationID:              moment.InvitationID,
		MomentTypeID:              moment.MomentTypeID,
		GuestID:                   moment.GuestID,
		Title:                     moment.Title,
		Description:               moment.Description,
		ContentURL:                moment.ContentURL,
		ThumbnailURL:              moment.ThumbnailURL,
		ContentURLExpiresAt:       moment.ContentURLExpiresAt,
		ThumbnailURLExpiresAt:     moment.ThumbnailURLExpiresAt,
		ContentViewURL:            moment.ContentURL,
		ThumbnailViewURL:          moment.ThumbnailURL,
		ContentViewURLExpiresAt:   moment.ContentURLExpiresAt,
		ThumbnailViewURLExpiresAt: moment.ThumbnailURLExpiresAt,
		ContentType:               moment.ContentType,
		IsApproved:                moment.IsApproved,
		ProcessingStatus:          moment.ProcessingStatus,
		ProcessingDurationMs:      moment.ProcessingDurationMs,
		OriginalSizeBytes:         moment.OriginalSizeBytes,
		OptimizedSizeBytes:        moment.OptimizedSizeBytes,
		ErrorMessage:              moment.ErrorMessage,
		Order:                     moment.Order,
		CreatedAt:                 moment.CreatedAt,
		UpdatedAt:                 moment.UpdatedAt,
	}
}

func NewMomentResponses(moments []models.Moment) []MomentResponse {
	response := make([]MomentResponse, 0, len(moments))
	for i := range moments {
		response = append(response, NewMomentResponse(&moments[i]))
	}
	return response
}
