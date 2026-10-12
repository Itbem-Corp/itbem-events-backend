package events

import (
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GST-1: page-spec by token and by identifier+token refuse a deleted guest's link.
func TestGetPageSpecByToken_RejectsInvitationWithoutLiveGuest(t *testing.T) {
	live := false
	deps := pageSpecDeps{
		getToken:               func(string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation:          func(uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		invitationHasLiveGuest: func(uuid.UUID) (bool, error) { return live, nil },
		getEvent:               func(uuid.UUID) (*models.Event, error) { return stubEvent(""), nil },
		getSections:            func(uuid.UUID) ([]models.EventSection, error) { return stubSections(), nil },
	}

	spec, err := getPageSpec(deps, "ABC123")
	assert.Nil(t, spec)
	assert.ErrorIs(t, err, ErrPageSpecTokenNotFound)

	live = true
	spec, err = getPageSpec(deps, "ABC123")
	require.NoError(t, err)
	require.Len(t, spec.Sections, 2)
}

func TestGetPageSpecByIdentifier_RejectsInvitationWithoutLiveGuest(t *testing.T) {
	deps := identifierPageSpecDeps{
		getEventByIdentifier:   func(string) (*models.Event, error) { return stubEvent(""), nil },
		getToken:               func(string) (*models.InvitationAccessToken, error) { return stubToken(), nil },
		getInvitation:          func(uuid.UUID) (*models.Invitation, error) { return stubInvitation(), nil },
		invitationHasLiveGuest: func(uuid.UUID) (bool, error) { return false, nil },
		getConfig:              func(uuid.UUID) (*models.EventConfig, error) { return &models.EventConfig{IsPublic: false}, nil },
		getSections: func(uuid.UUID) ([]models.EventSection, error) {
			t.Fatal("sections must not load for a deleted guest's token")
			return nil, nil
		},
	}

	spec, err := getPageSpecByIdentifier(deps, "grad-izapa-2025", "", "ABC123")
	assert.Nil(t, spec)
	assert.ErrorIs(t, err, ErrPageSpecTokenNotFound) // GST-36: revoked link
}
