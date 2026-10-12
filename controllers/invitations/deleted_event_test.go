package invitations

import (
	"net/http"
	"testing"

	"events-stocks/models"
	"events-stocks/services/ports"
	invitationsService "events-stocks/services/invitations"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// deletedEventRepo answers like the GORM repository for a soft-deleted event.
type deletedEventRepo struct{ ports.EventsRepository }

func (deletedEventRepo) GetEventByIDRaw(uuid.UUID) (*models.Event, error) {
	return nil, gorm.ErrRecordNotFound
}

func deletedEventInvitationService(invitationID, eventID uuid.UUID) *invitationsService.InvitationService {
	return invitationsService.NewInvitationServiceWithDeps(invitationsService.InvitationServiceDeps{
		Repo: &mockInvRepo{
			GetInvitationByIDLiteFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3}, nil
			},
			GetInvitationByIDFunc: func(id uuid.UUID) (*models.Invitation, error) {
				return &models.Invitation{ID: invitationID, EventID: eventID, MaxGuests: 3}, nil
			},
		},
		GuestRepo: &mockGuestRepo{},
		TokenRepo: &mockTokenRepo{
			GetByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
				return &models.InvitationAccessToken{ID: uuid.Must(uuid.NewV4()), InvitationID: invitationID, Token: token}, nil
			},
		},
		LogRepo:    &mockLogRepo{},
		Cache:      &mockCacheRepo{},
		EventsRepo: deletedEventRepo{},
	})
}

// EVT-9: a guest opening the link of a deleted event got 500 "Error loading
// event access"; it is a clean 410 now, for the invitation read and the RSVP.
func TestDeletedEventInvitationReturns410(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	orig := invitationSvc
	invitationSvc = deletedEventInvitationService(invitationID, eventID)
	defer func() { invitationSvc = orig }()

	c, rec := newEchoCtx(http.MethodPost, "/rsvp", `{"token":"RAW123","status":"confirmed","guest_count":1}`)
	require.NoError(t, ConfirmRSVP(c))
	assert.Equal(t, http.StatusGone, rec.Code)
	assert.Contains(t, rec.Body.String(), "Este evento ya no está disponible")

	c, rec = newEchoCtx(http.MethodGet, "/invitations/ByToken/RAW123", "")
	c.SetParamNames("token")
	c.SetParamValues("RAW123")
	require.NoError(t, GetInvitationByToken(c))
	assert.Equal(t, http.StatusGone, rec.Code)
}
