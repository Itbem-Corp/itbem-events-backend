package guests

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	guestsService "events-stocks/services/guests"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func guestWriteErrorCode(t *testing.T, body string) (string, string) {
	t.Helper()
	var envelope struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope), body)
	return envelope.Message, envelope.Error
}

// GST-7 for host writes: every free-text column is bounded; a value over its
// limit is a 400 before any write, never a raw SQLSTATE 22001.
func TestUpdateGuest_RejectsOverLongTextFields(t *testing.T) {
	for _, tc := range []struct{ field, value string }{
		{"dietary_restrictions", strings.Repeat("a", 256)},
		{"dietaryRestrictions", strings.Repeat("a", 256)},
		{"table_number", strings.Repeat("1", 61)},
		{"first_name", strings.Repeat("n", 201)},
		{"notes", strings.Repeat("n", 2001)},
		{"rsvp_notes", strings.Repeat("n", 1001)},
		{"phone", strings.Repeat("5", 51)},
		{"bio", strings.Repeat("b", 4001)},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newUpdateGuestFixture(t)
			code, body := f.put(t, `{"`+tc.field+`":"`+tc.value+`"}`)
			assert.Equal(t, http.StatusBadRequest, code, body)
			message, errCode := guestWriteErrorCode(t, body)
			assert.Equal(t, "guest_field_too_long", errCode)
			assert.Contains(t, message, "admite máximo")
			assert.Nil(t, f.repo.updated, "nothing is written")
		})
	}
}

func TestUpdateGuest_TextAtLimitIsAccepted(t *testing.T) {
	f := newUpdateGuestFixture(t)
	code, body := f.put(t, `{"dietary_restrictions":"`+strings.Repeat("é", 255)+`"}`)
	assert.Equal(t, http.StatusOK, code, body)
}

// A legacy row that already holds an over-long text keeps accepting edits of
// its other fields.
func TestUpdateGuest_LegacyLongTextDoesNotBlockOtherEdits(t *testing.T) {
	f := newUpdateGuestFixture(t)
	f.stored.Notes = strings.Repeat("x", 5000)
	f.repo.existing.Notes = f.stored.Notes
	code, body := f.put(t, `{"phone":"5512345678"}`)
	assert.Equal(t, http.StatusOK, code, body)
}

// GST-15: rsvp_status outside pending|confirmed|declined is refused.
func TestUpdateGuest_RejectsUnknownRSVPStatus(t *testing.T) {
	for _, body := range []string{`{"rsvp_status":"maybe"}`, `{"rsvpStatus":"ATTENDING"}`} {
		f := newUpdateGuestFixture(t)
		code, resp := f.put(t, body)
		assert.Equal(t, http.StatusBadRequest, code, resp)
		_, errCode := guestWriteErrorCode(t, resp)
		assert.Equal(t, "invalid_rsvp_status", errCode)
		assert.Nil(t, f.repo.updated)
	}
}

func TestUpdateGuest_AcceptsKnownRSVPStatusInAnyCase(t *testing.T) {
	for _, status := range []string{"pending", "DECLINED", " Confirmed "} {
		f := newUpdateGuestFixture(t)
		code, resp := f.put(t, `{"rsvp_status":"`+status+`"}`)
		assert.Equal(t, http.StatusOK, code, resp)
	}
}

// GST-15: the party size of a guest who is not declined cannot exceed
// max_guests, and both are bounded.
func TestUpdateGuest_RejectsPartySizeOverMaxGuests(t *testing.T) {
	f := newUpdateGuestFixture(t)
	f.stored.MaxGuests = 2
	f.repo.existing.MaxGuests = 2
	code, body := f.put(t, `{"guests_count":9}`)
	assert.Equal(t, http.StatusBadRequest, code, body)
	message, errCode := guestWriteErrorCode(t, body)
	assert.Equal(t, "party_size_exceeds_max_guests", errCode)
	assert.Contains(t, message, "(9)")
	assert.Contains(t, message, "(2)")
	assert.Nil(t, f.repo.updated)

	code, body = f.put(t, `{"guests_count":5,"max_guests":4}`)
	assert.Equal(t, http.StatusBadRequest, code, body)

	code, body = f.put(t, `{"guests_count":2}`)
	assert.Equal(t, http.StatusOK, code, body)
}

func TestUpdateGuest_RejectsOutOfRangePartySizes(t *testing.T) {
	for _, body := range []string{`{"max_guests":-1}`, `{"rsvp_guest_count":-2}`, `{"max_guests":101}`, `{"max_guests":150,"guests_count":150}`} {
		f := newUpdateGuestFixture(t)
		code, resp := f.put(t, body)
		assert.Equal(t, http.StatusBadRequest, code, resp)
		_, errCode := guestWriteErrorCode(t, resp)
		assert.Equal(t, "invalid_party_size", errCode, body)
	}
}

// A declined guest keeps saving even with a stale party size above the max.
func TestUpdateGuest_DeclinedGuestIsNotBlockedByStalePartySize(t *testing.T) {
	f := newUpdateGuestFixture(t)
	f.stored.RSVPStatus = "declined"
	f.repo.existing.RSVPStatus = "declined"
	f.stored.RSVPGuestCount = 4
	f.repo.existing.RSVPGuestCount = 4
	code, body := f.put(t, `{"max_guests":2}`)
	assert.Equal(t, http.StatusOK, code, body)
}

func TestCreateGuest_RejectsInvalidStatusAndPartySize(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()
	eventID := uuid.Must(uuid.NewV4()).String()

	for body, want := range map[string]string{
		`{"first_name":"Ana","event_id":"` + eventID + `","rsvp_status":"maybe"}`:                                     "invalid_rsvp_status",
		`{"first_name":"Ana","event_id":"` + eventID + `","guests_count":5,"max_guests":2}`:                           "party_size_exceeds_max_guests",
		`{"first_name":"Ana","event_id":"` + eventID + `","dietary_restrictions":"` + strings.Repeat("d", 256) + `"}`: "guest_field_too_long",
	} {
		c, rec := newEchoCtx(http.MethodPost, "/guests", body)
		setAuthzContext(t, c, uuid.Nil, nil)
		require.NoError(t, CreateGuest(c))
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		_, errCode := guestWriteErrorCode(t, rec.Body.String())
		assert.Equal(t, want, errCode)
	}
}

func TestCreateGuests_NamesTheRejectedRow(t *testing.T) {
	svc := guestsService.NewGuestService(&mockGuestRepo{}, &mockTokenRepo{}, &mockCacheRepo{}, &mockTransactor{})
	orig := guestSvc
	guestSvc = svc
	defer func() { guestSvc = orig }()
	eventID := uuid.Must(uuid.NewV4()).String()

	body := `[{"first_name":"Ana","event_id":"` + eventID + `"},{"first_name":"Bob","event_id":"` + eventID + `","notes":"` + strings.Repeat("n", 2001) + `"}]`
	c, rec := newEchoCtx(http.MethodPost, "/guests/bulk", body)
	setAuthzContext(t, c, uuid.Nil, nil)
	require.NoError(t, CreateGuests(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	message, errCode := guestWriteErrorCode(t, rec.Body.String())
	assert.Equal(t, "guest_field_too_long", errCode)
	assert.True(t, strings.HasPrefix(message, "Invitado 2:"), message)
}
