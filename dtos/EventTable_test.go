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

func TestNewEventTableResponseHidesEventRelation(t *testing.T) {
	tableID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 21, 0, 0, 0, time.UTC)

	body := NewEventTableResponse(models.EventTable{
		ID:        tableID,
		EventID:   eventID,
		Name:      "Mesa familia",
		Capacity:  10,
		SortOrder: 2,
		CreatedAt: createdAt,
		UpdatedAt: createdAt.Add(time.Minute),
		Event: models.Event{
			Identifier: "internal-event",
		},
	})

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"id":"`+tableID.String()+`",
		"event_id":"`+eventID.String()+`",
		"name":"Mesa familia",
		"capacity":10,
		"sort_order":2,
		"created_at":"2026-07-07T21:00:00Z",
		"updated_at":"2026-07-07T21:01:00Z"
	}`, string(encoded))
	assert.NotContains(t, string(encoded), "internal-event")
	assert.NotContains(t, string(encoded), "deleted_at")
}

func TestNewEventTableResponsesReturnsEmptyArray(t *testing.T) {
	body := NewEventTableResponses(nil)
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.JSONEq(t, `[]`, string(encoded))
}
