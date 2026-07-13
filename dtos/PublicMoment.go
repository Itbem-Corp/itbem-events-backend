package dtos

import (
	"events-stocks/models"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

// PublicMoment is the safe media shape exposed to the public event frontend.
type PublicMoment struct {
	ID                        uuid.UUID  `json:"id"`
	Title                     string     `json:"title,omitempty"`
	Description               string     `json:"description,omitempty"`
	ContentURL                string     `json:"content_url"`
	ThumbnailURL              string     `json:"thumbnail_url,omitempty"`
	ContentURLExpiresAt       *time.Time `json:"content_url_expires_at,omitempty"`
	ThumbnailURLExpiresAt     *time.Time `json:"thumbnail_url_expires_at,omitempty"`
	ContentViewURL            string     `json:"content_view_url,omitempty"`
	ThumbnailViewURL          string     `json:"thumbnail_view_url,omitempty"`
	ContentViewURLExpiresAt   *time.Time `json:"content_view_url_expires_at,omitempty"`
	ThumbnailViewURLExpiresAt *time.Time `json:"thumbnail_view_url_expires_at,omitempty"`
	ContentType               string     `json:"content_type,omitempty"`
	Order                     int        `json:"order,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	ApprovalStatus            string     `json:"approval_status,omitempty"`
	PublicationStatus         string     `json:"publication_status,omitempty"`
	ProcessingStatus          string     `json:"processing_status,omitempty"`
	ProcessingDurationMs      int64      `json:"processing_duration_ms,omitempty"`
	OriginalSizeBytes         int64      `json:"original_size_bytes,omitempty"`
	OptimizedSizeBytes        int64      `json:"optimized_size_bytes,omitempty"`
	ErrorMessage              string     `json:"error_message,omitempty"`
}

type PublicMomentsPage struct {
	Items                []PublicMoment `json:"items"`
	Total                int64          `json:"total"`
	Page                 int            `json:"page,omitempty"`
	Limit                int            `json:"limit,omitempty"`
	HasMore              bool           `json:"has_more"`
	NextCursor           string         `json:"next_cursor,omitempty"`
	Published            bool           `json:"published"`
	MomentsWallPublished bool           `json:"moments_wall_published"`
	ShowMomentWall       bool           `json:"show_moment_wall"`
	AllowUploads         bool           `json:"allow_uploads"`
	AllowMessages        bool           `json:"allow_messages"`
	ShareUploadsEnabled  bool           `json:"share_uploads_enabled"`
	UploadsLimit         int64          `json:"uploads_limit"`
	UploadsUsed          int64          `json:"uploads_used"`
	UploadsRemaining     int64          `json:"uploads_remaining"`
	EventName            string         `json:"event_name,omitempty"`
	EventType            string         `json:"event_type,omitempty"`
	EventDate            *time.Time     `json:"event_date,omitempty"`
	EventDateTime        *time.Time     `json:"event_date_time,omitempty"`
	Timezone             string         `json:"timezone,omitempty"`
}

type PublicMomentUploadResponse struct {
	PublicMoment
	UploadsLimit     int64 `json:"uploads_limit"`
	UploadsUsed      int64 `json:"uploads_used"`
	UploadsRemaining int64 `json:"uploads_remaining"`
}

func NewPublicMoment(moment models.Moment) PublicMoment {
	return PublicMoment{
		ID:                        moment.ID,
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
		Order:                     moment.Order,
		CreatedAt:                 moment.CreatedAt,
		ApprovalStatus:            publicMomentApprovalStatus(moment),
		PublicationStatus:         publicMomentPublicationStatus(moment),
		ProcessingStatus:          moment.ProcessingStatus,
		ProcessingDurationMs:      moment.ProcessingDurationMs,
		OriginalSizeBytes:         moment.OriginalSizeBytes,
		OptimizedSizeBytes:        moment.OptimizedSizeBytes,
		ErrorMessage:              moment.ErrorMessage,
	}
}

func publicMomentApprovalStatus(moment models.Moment) string {
	if moment.IsApproved {
		return "approved"
	}
	return "pending_review"
}

func publicMomentPublicationStatus(moment models.Moment) string {
	processingStatus := strings.ToLower(strings.TrimSpace(moment.ProcessingStatus))
	if processingStatus == "failed" {
		return "failed"
	}
	if !moment.IsApproved {
		return "pending_review"
	}
	if processingStatus == "pending" || processingStatus == "processing" {
		return "processing"
	}
	return "published"
}

func NewPublicMoments(moments []models.Moment) []PublicMoment {
	items := make([]PublicMoment, 0, len(moments))
	for _, moment := range moments {
		items = append(items, NewPublicMoment(moment))
	}
	return items
}
