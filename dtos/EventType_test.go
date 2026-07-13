package dtos

import (
	"encoding/json"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEventTypeResponseUsesSnakeCaseContract(t *testing.T) {
	eventTypeID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)

	body := NewEventTypeResponse(models.EventType{
		ID:        eventTypeID,
		Name:      "wedding",
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"id":"`+eventTypeID.String()+`",
		"name":"wedding",
		"created_at":"2026-07-07T18:00:00Z",
		"updated_at":"2026-07-07T19:00:00Z"
	}`, string(encoded))
	assert.NotContains(t, string(encoded), "DeletedAt")
	assert.NotContains(t, string(encoded), "CreatedAt")
	assert.NotContains(t, string(encoded), "UpdatedAt")
}

func TestNewEventTypeResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewEventTypeResponses(nil))
}
