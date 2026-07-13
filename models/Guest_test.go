package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuestAfterFindSetsDashboardAliases(t *testing.T) {
	guest := Guest{
		Invitation:     &Invitation{MaxGuests: 4},
		RSVPGuestCount: 2,
	}

	err := guest.AfterFind(nil)

	require.NoError(t, err)
	assert.Equal(t, 4, guest.MaxGuests)
	assert.Equal(t, 2, guest.GuestsCount)
}

func TestGuestAfterFindUsesInvitationMaxWhenRSVPIsEmpty(t *testing.T) {
	guest := Guest{
		Invitation: &Invitation{MaxGuests: 3},
	}

	err := guest.AfterFind(nil)

	require.NoError(t, err)
	assert.Equal(t, 3, guest.MaxGuests)
	assert.Equal(t, 3, guest.GuestsCount)
}

func TestGuestAfterFindUsesJoinedMaxWithoutHydratingInvitation(t *testing.T) {
	guest := Guest{MaxGuests: 5}

	err := guest.AfterFind(nil)

	require.NoError(t, err)
	assert.Nil(t, guest.Invitation)
	assert.Equal(t, 5, guest.MaxGuests)
	assert.Equal(t, 5, guest.GuestsCount)
}

func TestGuestAfterFindUsesZeroPartySizeForDeclinedRSVP(t *testing.T) {
	guest := Guest{
		Invitation: &Invitation{MaxGuests: 3},
		RSVPStatus: "declined",
	}

	err := guest.AfterFind(nil)

	require.NoError(t, err)
	assert.Equal(t, 3, guest.MaxGuests)
	assert.Equal(t, 0, guest.GuestsCount)

	payload, err := json.Marshal(guest)
	require.NoError(t, err)
	assert.Contains(t, string(payload), `"guests_count":0`)
}

func TestGuestAfterFindDefaultsPartySizeToOne(t *testing.T) {
	guest := Guest{}

	err := guest.AfterFind(nil)

	require.NoError(t, err)
	assert.Equal(t, 1, guest.GuestsCount)
}
