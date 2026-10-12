package invitations

import (
	"testing"
	"time"

	"events-stocks/internal/publicaccessproof"
	"events-stocks/models"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gateFixture struct {
	eventID      uuid.UUID
	invitationID uuid.UUID
	cfg          *models.EventConfig
	now          time.Time
	guestUpdated bool
}

func newGateFixture() *gateFixture {
	eventID := uuid.Must(uuid.NewV4())
	return &gateFixture{
		eventID:      eventID,
		invitationID: uuid.Must(uuid.NewV4()),
		cfg:          &models.EventConfig{ID: eventID, IsPublic: false, VisibilityConfigured: true, ShowRSVPSection: true},
		now:          time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	}
}

func (f *gateFixture) service() *InvitationService {
	tokenRepo := &mockAccessTokenRepo{
		GetByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{ID: uuid.Must(uuid.NewV4()), InvitationID: f.invitationID, PrettyToken: code}, nil
		},
	}
	invitation := func(uuid.UUID) (*models.Invitation, error) {
		return &models.Invitation{
			ID: f.invitationID, EventID: f.eventID, MaxGuests: 3,
			Event: models.Event{ID: f.eventID, Name: "Boda", IsActive: true},
		}, nil
	}
	invRepo := &mockInvitationRepo{GetInvitationByIDFunc: invitation, GetInvitationByIDLiteFunc: invitation}
	guestRepo := &mockGuestRepo{
		GetGuestByInvitationIDFunc: func(uuid.UUID) (*models.Guest, error) {
			id := f.invitationID
			return &models.Guest{ID: uuid.Must(uuid.NewV4()), EventID: f.eventID, InvitationID: &id, FirstName: "Ana", RSVPStatus: "confirmed"}, nil
		},
		UpdateGuestFunc: func(*models.Guest) error { f.guestUpdated = true; return nil },
	}
	eventsRepo := &mockEventsRepo{
		GetEventByIDRawFunc: func(id uuid.UUID) (*models.Event, error) { return &models.Event{ID: id, IsActive: true}, nil },
	}
	configRepo := &mockEventConfigRepo{
		GetEventConfigByIDFunc: func(uuid.UUID) (*models.EventConfig, error) { return f.cfg, nil },
	}
	return newServiceWithPublicAccess(invRepo, guestRepo, tokenRepo, nil, nil, eventsRepo, configRepo, func() time.Time { return f.now })
}

func (f *gateFixture) proof(t *testing.T) string {
	t.Helper()
	proof, _, err := publicaccessproof.Generate(f.eventID, eventsService.EventConfigAccessVersion(f.cfg), time.Hour)
	require.NoError(t, err)
	return proof
}

// SET-12: a password-protected event no longer leaks its meta through ByToken.
func TestGetInvitationByToken_RequiresPasswordProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	f := newGateFixture()
	f.cfg.AuthPasswordPreview = "secreto"
	svc := f.service()

	result, err := svc.GetInvitationByTokenWithAccess("ABC123", PublicAccess{})
	assert.ErrorIs(t, err, ErrInvitationPasswordRequired)
	assert.Nil(t, result)

	_, err = svc.GetInvitationByTokenWithAccess("ABC123", PublicAccess{PasswordProof: "forged.proof"})
	assert.ErrorIs(t, err, ErrInvitationPasswordRequired)

	result, err = svc.GetInvitationByTokenWithAccess("ABC123", PublicAccess{PasswordProof: f.proof(t)})
	require.NoError(t, err)
	require.NotNil(t, result.Event)
	assert.Equal(t, "Boda", result.Event.Name)
}

func TestConfirmRSVP_RequiresPasswordProof(t *testing.T) {
	t.Setenv("EVENT_ACCESS_SECRET", "test-secret")
	f := newGateFixture()
	f.cfg.AuthPasswordPreview = "secreto"
	svc := f.service()

	_, err := svc.ConfirmRSVPWithAccess(PublicAccess{}, "ABC123", "confirmed", "web", 1, "")
	assert.ErrorIs(t, err, ErrInvitationPasswordRequired)
	assert.False(t, f.guestUpdated)

	_, err = svc.ConfirmRSVPWithAccess(PublicAccess{PasswordProof: f.proof(t)}, "ABC123", "confirmed", "web", 1, "")
	require.NoError(t, err)
	assert.True(t, f.guestUpdated)
}

func TestConfirmRSVP_RejectsAfterDeadlineAndWhenRSVPHidden(t *testing.T) {
	f := newGateFixture()
	deadline := f.now.Add(-time.Minute)
	f.cfg.RSVPDeadline = &deadline
	_, err := f.service().ConfirmRSVPWithAccess(PublicAccess{}, "ABC123", "declined", "web", 0, "")
	assert.ErrorIs(t, err, ErrRSVPClosed)
	assert.False(t, f.guestUpdated)

	f = newGateFixture()
	f.cfg.ShowRSVPSection = false
	f.cfg.ShowHeader = true // explicit visibility: RSVP block hidden on purpose
	_, err = f.service().ConfirmRSVPWithAccess(PublicAccess{}, "ABC123", "confirmed", "web", 1, "")
	assert.ErrorIs(t, err, ErrRSVPClosed)
	assert.False(t, f.guestUpdated)
}

func TestConfirmRSVP_AcceptsBeforeDeadline(t *testing.T) {
	f := newGateFixture()
	deadline := f.now.Add(time.Minute)
	f.cfg.RSVPDeadline = &deadline
	_, err := f.service().ConfirmRSVPWithAccess(PublicAccess{}, "ABC123", "confirmed", "web", 2, "")
	require.NoError(t, err)
	assert.True(t, f.guestUpdated)
}

func TestConfirmRSVP_ClosedWindowStillBlocks(t *testing.T) {
	f := newGateFixture()
	until := f.now.Add(-time.Hour)
	f.cfg.ActiveUntil = &until
	_, err := f.service().ConfirmRSVPWithAccess(PublicAccess{}, "ABC123", "confirmed", "web", 1, "")
	assert.ErrorIs(t, err, ErrInvitationEventUnavailable)
	assert.False(t, f.guestUpdated)
}

func TestGetInvitationByToken_ExposesRSVPAvailability(t *testing.T) {
	f := newGateFixture()
	deadline := f.now.Add(-time.Minute)
	f.cfg.RSVPDeadline = &deadline
	result, err := f.service().GetInvitationByTokenWithAccess("ABC123", PublicAccess{})
	require.NoError(t, err, "reading the invitation stays allowed after the deadline")
	require.NotNil(t, result.Event)
	require.NotNil(t, result.Event.RSVPDeadline)
	assert.True(t, result.Event.RSVPDeadline.Equal(deadline))
	assert.False(t, result.Event.RSVPOpen)

	f = newGateFixture()
	result, err = f.service().GetInvitationByTokenWithAccess("ABC123", PublicAccess{})
	require.NoError(t, err)
	assert.Nil(t, result.Event.RSVPDeadline)
	assert.True(t, result.Event.RSVPOpen)
}
