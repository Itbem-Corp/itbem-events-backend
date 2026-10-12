package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupVerifyAccessLimitTest(t *testing.T) uuid.UUID {
	t.Helper()
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	restoreEventSvc := eventSvc
	restoreEventConfigSvc := eventConfigSvc
	restoreTokenRepo := eventAccessTokenRepo
	restoreInvitationRepo := eventInvitationRepo
	restoreLimiter := eventPasswordLimiter
	t.Cleanup(func() {
		eventSvc = restoreEventSvc
		eventConfigSvc = restoreEventConfigSvc
		eventAccessTokenRepo = restoreTokenRepo
		eventInvitationRepo = restoreInvitationRepo
		eventPasswordLimiter = restoreLimiter
	})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	eventPasswordLimiter = publicaccessproof.NewAttemptLimiter(nil, func() time.Time { return now })

	eventID := uuid.Must(uuid.NewV4())
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return &models.Event{ID: eventID, Identifier: identifier, IsActive: true}, nil
		},
	}, &mockCacheRepo{})
	eventConfigSvc = eventsService.NewEventConfigService(&mockEventConfigRepo{
		cfg: &models.EventConfig{ID: eventID, IsPublic: true, AuthPasswordPreview: "secreto"},
	}, &mockCacheRepo{})
	eventAccessTokenRepo = nil
	eventInvitationRepo = nil
	return eventID
}

func verifyAccessFrom(t *testing.T, ip, password string) (int, string, http.Header) {
	t.Helper()
	c, rec := newEchoCtx(http.MethodPost, "/api/events/evento/verify-access", fmt.Sprintf(`{"password":%q}`, password))
	c.Request().Header.Set("X-Real-IP", ip)
	c.SetParamNames("identifier")
	c.SetParamValues("evento")
	require.NoError(t, VerifyEventAccess(c))
	return rec.Code, rec.Body.String(), rec.Header()
}

func TestVerifyEventAccess_RateLimitsGuessesPerEventAndIP(t *testing.T) {
	setupVerifyAccessLimitTest(t)

	for i := 0; i < publicaccessproof.PasswordAttemptsPerIP; i++ {
		code, _, _ := verifyAccessFrom(t, "203.0.113.7", fmt.Sprintf("guess-%d", i))
		require.Equal(t, http.StatusUnauthorized, code, "attempt %d", i+1)
	}

	code, body, header := verifyAccessFrom(t, "203.0.113.7", "secreto")
	assert.Equal(t, http.StatusTooManyRequests, code, "even the right password is refused once the IP budget is spent")
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	assert.Equal(t, float64(http.StatusTooManyRequests), payload["status"])
	assert.Equal(t, "too_many_attempts", payload["code"])
	assert.Equal(t, "Demasiados intentos. Espera unos minutos e inténtalo de nuevo.", payload["message"])
	assert.Equal(t, "600", header.Get("Retry-After"))
	assert.NotContains(t, body, "accessToken")

	code, body, _ = verifyAccessFrom(t, "198.51.100.9", "secreto")
	assert.Equal(t, http.StatusOK, code, "another guest is not affected")
	assert.Contains(t, body, `"accessToken"`)
}

func TestVerifyEventAccess_RateLimitsFailuresPerEventAcrossIPs(t *testing.T) {
	setupVerifyAccessLimitTest(t)

	for i := 0; i < publicaccessproof.PasswordFailuresPerEvent; i++ {
		code, _, _ := verifyAccessFrom(t, fmt.Sprintf("10.0.%d.%d", i/250, i%250+1), "wrong-pass")
		require.Equal(t, http.StatusUnauthorized, code, "failure %d", i+1)
	}

	code, body, header := verifyAccessFrom(t, "192.0.2.200", "secreto")
	assert.Equal(t, http.StatusTooManyRequests, code)
	assert.Contains(t, body, `"code":"too_many_attempts"`)
	assert.Equal(t, "900", header.Get("Retry-After"))
}

func TestVerifyEventAccess_SuccessfulGuestsDoNotConsumeEventBudget(t *testing.T) {
	setupVerifyAccessLimitTest(t)

	for i := 0; i < publicaccessproof.PasswordFailuresPerEvent+20; i++ {
		code, _, _ := verifyAccessFrom(t, fmt.Sprintf("10.1.%d.%d", i/250, i%250+1), "secreto")
		require.Equal(t, http.StatusOK, code, "guest %d", i+1)
	}
}
