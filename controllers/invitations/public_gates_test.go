package invitations

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"
	invitationsService "events-stocks/services/invitations"
	"events-stocks/utils"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installGateService(t *testing.T, cfg *models.EventConfig) (eventID uuid.UUID, updated *bool) {
	t.Helper()
	eventID = cfg.ID
	invitationID := uuid.Must(uuid.NewV4())
	guestUpdated := false
	invitation := func(uuid.UUID) (*models.Invitation, error) {
		return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3, Event: models.Event{ID: eventID, Name: "Boda"}}, nil
	}
	svc := invitationsService.NewInvitationServiceWithDeps(invitationsService.InvitationServiceDeps{
		Repo: &mockInvRepo{GetInvitationByIDFunc: invitation, GetInvitationByIDLiteFunc: invitation},
		GuestRepo: &mockGuestRepo{
			GetGuestByInvitationIDFunc: func(uuid.UUID) (*models.Guest, error) {
				return &models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: eventID, InvitationID: &invitationID, RSVPStatus: "confirmed"}, nil
			},
			UpdateGuestFunc: func(*models.Guest) error { guestUpdated = true; return nil },
		},
		TokenRepo: &mockTokenRepo{
			GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{ID: uuid.Must(uuid.NewV4()), InvitationID: invitationID, PrettyToken: code}, nil
			},
		},
		LogRepo:    &mockLogRepo{},
		Cache:      &mockCacheRepo{},
		ConfigRepo: &mockEventConfigRepo{GetEventConfigByIDFunc: func(uuid.UUID) (*models.EventConfig, error) { return cfg, nil }},
	})
	orig := invitationSvc
	invitationSvc = svc
	t.Cleanup(func() { invitationSvc = orig })
	return eventID, &guestUpdated
}

func decodePublicError(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload
}

func TestConfirmRSVP_AfterDeadlineReturns403RSVPClosed(t *testing.T) {
	deadline := time.Now().Add(-time.Hour)
	_, updated := installGateService(t, &models.EventConfig{ID: uuid.Must(uuid.NewV4()), RSVPDeadline: &deadline})

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{"pretty_token":"ABC123","status":"confirmed","guest_count":1}`)
	require.NoError(t, ConfirmRSVP(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	payload := decodePublicError(t, rec.Body.Bytes())
	assert.Equal(t, float64(403), payload["status"])
	assert.Equal(t, "rsvp_closed", payload["code"])
	assert.Equal(t, "rsvp_closed", payload["error"], "the code is also in error (S1)")
	assert.Equal(t, "Las confirmaciones para este evento ya están cerradas.", payload["message"])
	assert.False(t, *updated)
}

func TestConfirmRSVP_PasswordEventWithoutProofReturns401(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	cfg := &models.EventConfig{ID: uuid.Must(uuid.NewV4()), AuthPasswordPreview: "secreto"}
	eventID, updated := installGateService(t, cfg)

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{"pretty_token":"ABC123","status":"confirmed","guest_count":1}`)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "event_password_required", decodePublicError(t, rec.Body.Bytes())["code"])
	assert.Equal(t, "event_password_required", decodePublicError(t, rec.Body.Bytes())["error"])
	assert.False(t, *updated)

	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)
	c, rec = newEchoCtx(http.MethodPost, "/rsvp", `{"pretty_token":"ABC123","status":"confirmed","guest_count":1}`)
	c.Request().Header.Set(utils.HeaderEventAccessToken, proof)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, *updated)
}

func TestGetInvitationByToken_PasswordEventWithoutProofReturns401(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	cfg := &models.EventConfig{ID: uuid.Must(uuid.NewV4()), AuthPasswordPreview: "secreto"}
	eventID, _ := installGateService(t, cfg)

	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken?token=ABC123", "")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "Boda")

	proof, _, err := publicaccessproof.Generate(eventID, eventsService.EventConfigAccessVersion(cfg), time.Hour)
	require.NoError(t, err)
	c, rec = newEchoCtx(http.MethodGet, "/invitations/ByToken?token=ABC123&event_access_token="+proof, "")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"rsvp_open":true`)
	assert.Contains(t, rec.Body.String(), `"rsvp_deadline":null`)
}

// EVT-54: RSVP with the access window closed answers a Spanish message and
// the event_unavailable code instead of "Event is not public".
func TestConfirmRSVP_ClosedAccessWindowReturnsEventUnavailable(t *testing.T) {
	ended := time.Now().Add(-time.Hour)
	_, updated := installGateService(t, &models.EventConfig{ID: uuid.Must(uuid.NewV4()), ActiveUntil: &ended})

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{"pretty_token":"ABC123","status":"confirmed","guest_count":1}`)
	require.NoError(t, ConfirmRSVP(c))

	assert.Equal(t, http.StatusForbidden, rec.Code)
	payload := decodePublicError(t, rec.Body.Bytes())
	assert.Equal(t, EventUnavailableCode, payload["code"])
	assert.Equal(t, EventUnavailableMessage, payload["message"])
	assert.False(t, *updated)
}

func TestEventGoneUsesThePublicEnvelope(t *testing.T) {
	c, rec := newEchoCtx(http.MethodGet, "/invitations/ByToken/ABC123", "")
	require.NoError(t, eventGone(c))
	assert.Equal(t, http.StatusGone, rec.Code)
	payload := decodePublicError(t, rec.Body.Bytes())
	assert.Equal(t, "event_gone", payload["code"])
	assert.Equal(t, "event_gone", payload["error"])
	assert.Equal(t, "Este evento ya no está disponible", payload["message"])
}
