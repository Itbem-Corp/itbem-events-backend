package invitations

import (
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
)

type catalogGuestRepo struct {
	*mockGuestRepo
	ids map[string]uuid.UUID
}

func (r *catalogGuestRepo) GuestStatusIDByCode(code string) (uuid.UUID, error) {
	return r.ids[code], nil
}

// GST-62 / PUB-64: a web RSVP moves guest_status_id with rsvp_status.
func TestSyncGuestStatusWithRSVP(t *testing.T) {
	confirmed := uuid.Must(uuid.NewV4())
	pending := uuid.Must(uuid.NewV4())
	repo := &catalogGuestRepo{mockGuestRepo: &mockGuestRepo{}, ids: map[string]uuid.UUID{"confirmed": confirmed}}
	guest := &models.Guest{GuestStatusID: pending, StatusID: pending}

	syncGuestStatusWithRSVP(repo, guest, "confirmed")

	assert.Equal(t, confirmed, guest.GuestStatusID)
	assert.Equal(t, confirmed, guest.StatusID)

	// Without a catalog the columns stay as they were.
	plain := &models.Guest{GuestStatusID: pending, StatusID: pending}
	syncGuestStatusWithRSVP(&mockGuestRepo{}, plain, "confirmed")
	assert.Equal(t, pending, plain.GuestStatusID)
}
