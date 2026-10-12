package moments

import (
	"fmt"
	"net/http"
	"testing"

	"events-stocks/models"
	momentsService "events-stocks/services/moments"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// MOM-6: when a concurrent confirm of the same upload wins the unique index,
// the loser answers with the winner's moment, gives back its quota slot and
// keeps the shared object.
func TestConfirmPresignedMomentLosingTheRaceReturnsTheWinner(t *testing.T) {
	eventID := uuid.Must(uuid.NewV4())
	key := "moments/" + eventID.String() + "/raw/photo.jpg"
	winner := &models.Moment{ID: uuid.Must(uuid.NewV4()), EventID: &eventID, ContentURL: key}
	lookups := 0
	repo := &mockMomentRepo{
		GetByEventAndContentURLFunc: func(uuid.UUID, string) (*models.Moment, error) {
			lookups++
			if lookups == 1 {
				return nil, gorm.ErrRecordNotFound // before the race
			}
			return winner, nil
		},
		CreateMomentFunc: func(*models.Moment) error {
			return fmt.Errorf("%w: duplicate key", momentsService.ErrDuplicateMomentContent)
		},
	}
	storage := &mockObjectStorage{}
	counter := &mockUploadCounter{value: "0"}
	setupConfirmMomentTest(t, repo, storage, counter)
	c, _ := newEchoCtx(http.MethodPost, "/confirm", "")

	moment, err := confirmPresignedMoment(c, &models.Event{ID: eventID}, &models.EventConfig{MaxUploadsPerGuest: 5}, nil, key, "image/jpeg", 0, "")

	require.NoError(t, err)
	require.NotNil(t, moment)
	assert.Equal(t, winner.ID, moment.ID)
	assert.Equal(t, 1, counter.increments)
	assert.Equal(t, 1, counter.decrements, "the loser's quota slot is given back")
	assert.Empty(t, storage.deletedObjects, "the shared object is kept")
}
