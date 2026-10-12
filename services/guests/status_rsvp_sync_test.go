package guests

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

func newCatalogRepo() *catalogGuestRepo {
	return &catalogGuestRepo{mockGuestRepo: &mockGuestRepo{}, ids: map[string]uuid.UUID{
		"pending":   uuid.Must(uuid.NewV4()),
		"confirmed": uuid.Must(uuid.NewV4()),
		"declined":  uuid.Must(uuid.NewV4()),
	}}
}

// GST-15/GST-62: a PUT that only changes rsvp_status moves the status too.
func TestAlignGuestStatusWithRSVP_RSVPChangeMovesStatus(t *testing.T) {
	repo := newCatalogRepo()
	svc := NewGuestService(repo, nil, nil, nil)
	old := &models.Guest{RSVPStatus: "pending", GuestStatusID: repo.ids["pending"]}
	next := *old
	next.StatusID = repo.ids["pending"] // as loaded (AfterFind)
	next.RSVPStatus = "confirmed"

	svc.alignGuestStatusWithRSVP(&next, old)

	assert.Equal(t, repo.ids["confirmed"], next.GuestStatusID)
	assert.Equal(t, repo.ids["confirmed"], next.StatusID)
}

// A PUT that only changes status_id moves rsvp_status to the matching code.
func TestAlignGuestStatusWithRSVP_StatusChangeMovesRSVP(t *testing.T) {
	repo := newCatalogRepo()
	svc := NewGuestService(repo, nil, nil, nil)
	old := &models.Guest{RSVPStatus: "pending", GuestStatusID: repo.ids["pending"]}
	next := *old
	next.StatusID = repo.ids["declined"]
	next.GuestStatusID = repo.ids["declined"]

	svc.alignGuestStatusWithRSVP(&next, old)

	assert.Equal(t, "declined", next.RSVPStatus)
}

// Both changed and disagreeing: rsvp_status wins.
func TestAlignGuestStatusWithRSVP_RSVPWinsOnConflict(t *testing.T) {
	repo := newCatalogRepo()
	svc := NewGuestService(repo, nil, nil, nil)
	old := &models.Guest{RSVPStatus: "pending", GuestStatusID: repo.ids["pending"]}
	next := *old
	next.RSVPStatus = "confirmed"
	next.StatusID = repo.ids["declined"]
	next.GuestStatusID = repo.ids["declined"]

	svc.alignGuestStatusWithRSVP(&next, old)

	assert.Equal(t, repo.ids["confirmed"], next.StatusID)
	assert.Equal(t, "confirmed", next.RSVPStatus)
}
