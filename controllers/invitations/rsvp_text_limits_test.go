package invitations

import (
	"errors"
	"events-stocks/models"
	invitationsService "events-stocks/services/invitations"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GST-7: an over-long free-text RSVP field is a 400 with a Spanish message,
// never a write that fails on varchar(255) and comes back as a 401 with the
// Postgres error.
func TestConfirmRSVP_DietaryOverLimitReturns400WithoutWriting(t *testing.T) {
	writes := 0
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{UpdateGuestFunc: func(g *models.Guest) error { writes++; return nil }},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	for name, body := range map[string]string{
		"snake dietary": `{"pretty_token":"ABC123","status":"confirmed","guest_count":1,"dietary_restrictions":"` + strings.Repeat("a", 256) + `"}`,
		"legacy notes":  `{"pretty_token":"ABC123","status":"confirmed","guest_count":1,"notes":"` + strings.Repeat("ñ", 256) + `"}`,
		"rsvp notes":    `{"pretty_token":"ABC123","status":"confirmed","guest_count":1,"dietary_restrictions":"","rsvp_notes":"` + strings.Repeat("x", 1001) + `"}`,
		"camel dietary": `{"prettyToken":"ABC123","status":"confirmed","guestCount":1,"dietaryRestrictions":"` + strings.Repeat("b", 300) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
			require.NoError(t, ConfirmRSVP(c))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "admite máximo")
			assert.Contains(t, rec.Body.String(), `"error":"rsvp_text_too_long"`)
		})
	}
	assert.Zero(t, writes)
}

func TestConfirmRSVP_DietaryAtLimitIsAccepted(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{}, &mockGuestRepo{}, &mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	// 255 multi-byte characters: the limit is characters, as varchar counts.
	body := `{"pretty_token":"ABC123","status":"confirmed","guest_count":1,"dietary_restrictions":"` + strings.Repeat("é", 255) + `"}`
	c, rec := newEchoCtx(http.MethodPost, "/rsvp", body)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// A persistence failure is a 500 with a fixed Spanish message: the database
// error text is never echoed and the status is no longer 401.
func TestConfirmRSVP_PersistenceErrorIs500WithoutLeak(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{UpdateGuestFunc: func(g *models.Guest) error {
			return errors.New("ERROR: value too long for type character varying(255) (SQLSTATE 22001)")
		}},
		&mockTokenRepo{}, &mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{"pretty_token":"ABC123","status":"confirmed","guest_count":1}`)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "SQLSTATE")
	assert.Contains(t, rec.Body.String(), `"error":"rsvp_save_failed"`)
}

func TestConfirmRSVP_UnknownTokenStays401(t *testing.T) {
	svc := invitationsService.NewInvitationService(
		&mockInvRepo{},
		&mockGuestRepo{},
		&mockTokenRepo{GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("record not found")
		}, GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("record not found")
		}},
		&mockLogRepo{}, &mockCacheRepo{},
	)
	orig := invitationSvc
	invitationSvc = svc
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{"pretty_token":"NOPE0000","status":"confirmed","guest_count":1}`)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"invalid or expired token"`)
	assert.NotContains(t, rec.Body.String(), "record not found")
}
