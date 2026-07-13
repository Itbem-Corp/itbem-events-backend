package dtos

import (
	"encoding/json"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewMomentResponseUsesAdminDashboardContract(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())
	guestID := uuid.Must(uuid.NewV4())
	momentTypeID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, time.July, 7, 10, 0, 0, 0, time.UTC)
	contentExpiresAt := createdAt.Add(12 * time.Minute)
	thumbnailExpiresAt := createdAt.Add(10 * time.Minute)

	body := NewMomentResponse(&models.Moment{
		ID:                    uuid.Must(uuid.NewV4()),
		EventID:               &eventID,
		InvitationID:          &invitationID,
		Invitation:            models.Invitation{ID: invitationID},
		MomentTypeID:          &momentTypeID,
		MomentType:            models.MomentType{ID: momentTypeID, Name: "image"},
		GuestID:               &guestID,
		Guest:                 &models.Guest{ID: guestID, Email: "guest@example.com"},
		Title:                 "Entrada",
		Description:           "Foto bonita",
		ContentURL:            "https://signed.example.com/photo.webp",
		ThumbnailURL:          "https://signed.example.com/thumb.webp",
		ContentURLExpiresAt:   &contentExpiresAt,
		ThumbnailURLExpiresAt: &thumbnailExpiresAt,
		ContentType:           "image/webp",
		IsApproved:            true,
		ProcessingStatus:      "done",
		ProcessingDurationMs:  1500,
		OriginalSizeBytes:     2048,
		OptimizedSizeBytes:    1024,
		Order:                 3,
		CreatedAt:             createdAt,
		UpdatedAt:             createdAt.Add(time.Minute),
		DeletedAt:             gorm.DeletedAt{Time: createdAt, Valid: true},
	})

	assert.Equal(t, &eventID, body.EventID)
	assert.Equal(t, &invitationID, body.InvitationID)
	assert.Equal(t, &guestID, body.GuestID)
	assert.Equal(t, &momentTypeID, body.MomentTypeID)
	assert.Equal(t, "https://signed.example.com/photo.webp", body.ContentURL)
	assert.Equal(t, "https://signed.example.com/photo.webp", body.ContentViewURL)
	assert.Equal(t, &contentExpiresAt, body.ContentViewURLExpiresAt)
	assert.Equal(t, "https://signed.example.com/thumb.webp", body.ThumbnailViewURL)
	assert.Equal(t, &thumbnailExpiresAt, body.ThumbnailViewURLExpiresAt)
	assert.True(t, body.IsApproved)
	assert.Equal(t, int64(1024), body.OptimizedSizeBytes)

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "guest@example.com")
	assert.NotContains(t, string(raw), "guest\":")
	assert.NotContains(t, string(raw), "invitation\":")
	assert.NotContains(t, string(raw), "moment_type\":")
	assert.NotContains(t, string(raw), "deleted_at")
}

func TestNewMomentResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewMomentResponses(nil))
}
