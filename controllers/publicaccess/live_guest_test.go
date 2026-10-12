package publicaccess

import (
	"errors"
	"testing"

	"events-stocks/models"
	"events-stocks/repositories/invitationrepository"
	eventsService "events-stocks/services/events"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production invitation repository must expose the live-guest check, or
// the GST-1 gate silently degrades to "allow".
var _ eventsService.InvitationLiveGuestChecker = (*invitationrepository.InvitationRepo)(nil)

type liveGuestInvitationRepo struct {
	mockInvitationRepo
	live bool
	err  error
	seen uuid.UUID
}

func (m *liveGuestInvitationRepo) InvitationHasLiveGuest(id uuid.UUID) (bool, error) {
	m.seen = id
	return m.live, m.err
}

func privateEventDeps(eventID, invitationID uuid.UUID, repo *liveGuestInvitationRepo) EventReadDeps {
	repo.invitation = &models.Invitation{ID: invitationID, EventID: eventID}
	return EventReadDeps{
		ConfigRepo:     &mockConfigRepo{cfg: &models.EventConfig{ID: eventID, IsPublic: false}},
		TokenRepo:      &mockTokenRepo{token: &models.InvitationAccessToken{InvitationID: invitationID}},
		InvitationRepo: repo,
		IsEventActive:  func(uuid.UUID) (bool, error) { return true, nil },
	}
}

// GST-1: a deleted guest's link must not open a private event.
func TestAllowEventRead_DeniesInvitationTokenOfDeletedGuest(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	invitationID := uuid.Must(uuid.NewV4())

	repo := &liveGuestInvitationRepo{live: false}
	allowed, err := AllowEventRead(eventID, "", "ABC123", privateEventDeps(eventID, invitationID, repo))
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, invitationID, repo.seen)

	repo = &liveGuestInvitationRepo{live: true}
	allowed, err = AllowEventRead(eventID, "", "ABC123", privateEventDeps(eventID, invitationID, repo))
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestAllowEventRead_DeniesInvitationTokenWhenLiveGuestLookupFails(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	repo := &liveGuestInvitationRepo{live: true, err: errors.New("db down")}
	allowed, err := AllowEventRead(eventID, "", "ABC123", privateEventDeps(eventID, uuid.Must(uuid.NewV4()), repo))
	require.NoError(t, err)
	assert.False(t, allowed)
}
