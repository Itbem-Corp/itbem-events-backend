package models

import (
	"encoding/json"
	"testing"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/require"
)

func TestEventAnalyticsJSONContract(t *testing.T) {
	id := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())

	payload, err := json.Marshal(EventAnalytics{
		ID:             id,
		EventID:        eventID,
		Event:          Event{ID: uuid.Must(uuid.NewV4())},
		Views:          7,
		MomentUploads:  3,
		MomentComments: 1,
		MomentTotal:    3,
		MomentApproved: 2,
		MomentPending:  1,
		RSVPConfirmed:  5,
		RSVPDeclined:   2,
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(payload, &body))

	require.Equal(t, id.String(), body["id"])
	require.Equal(t, eventID.String(), body["event_id"])
	require.Equal(t, float64(7), body["views"])
	require.Equal(t, float64(3), body["moment_uploads"])
	require.Equal(t, float64(1), body["moment_comments"])
	require.Equal(t, float64(3), body["moment_total"])
	require.Equal(t, float64(2), body["moment_approved"])
	require.Equal(t, float64(1), body["moment_pending"])
	require.Equal(t, float64(5), body["rsvp_confirmed"])
	require.Equal(t, float64(2), body["rsvp_declined"])
	require.NotContains(t, body, "ID")
	require.NotContains(t, body, "EventID")
	require.NotContains(t, body, "Event")
	require.NotContains(t, body, "event")
}
