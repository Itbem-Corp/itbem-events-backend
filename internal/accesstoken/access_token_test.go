package accesstoken

import (
	"errors"
	"testing"

	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockLookupRepo struct {
	getByTokenFunc       func(token string) (*models.InvitationAccessToken, error)
	getByPrettyTokenFunc func(code string) (*models.InvitationAccessToken, error)
	rawSeen              string
	prettySeen           string
}

func (m *mockLookupRepo) GetByToken(token string) (*models.InvitationAccessToken, error) {
	m.rawSeen = token
	if m.getByTokenFunc != nil {
		return m.getByTokenFunc(token)
	}
	return nil, nil
}

func (m *mockLookupRepo) GetByPrettyToken(code string) (*models.InvitationAccessToken, error) {
	m.prettySeen = code
	if m.getByPrettyTokenFunc != nil {
		return m.getByPrettyTokenFunc(code)
	}
	return nil, nil
}

func (m *mockLookupRepo) GeneratePrettyToken(eventID uuid.UUID, length int) (string, error) {
	return "ABC123", nil
}

func TestLookupUsesTrimmedRawTokenFirst(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	repo := &mockLookupRepo{
		getByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{InvitationID: invitationID, Token: token}, nil
		},
	}

	token, err := Lookup(repo, "  raw-token  ")

	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, invitationID, token.InvitationID)
	assert.Equal(t, "raw-token", repo.rawSeen)
	assert.Empty(t, repo.prettySeen)
}

func TestLookupFallsBackToPrettyToken(t *testing.T) {
	invitationID := uuid.Must(uuid.NewV4())
	repo := &mockLookupRepo{
		getByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return nil, errors.New("not found")
		},
		getByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return &models.InvitationAccessToken{InvitationID: invitationID, PrettyToken: code}, nil
		},
	}

	token, err := Lookup(repo, "PRETTY123")

	require.NoError(t, err)
	require.NotNil(t, token)
	assert.Equal(t, invitationID, token.InvitationID)
	assert.Equal(t, "PRETTY123", repo.rawSeen)
	assert.Equal(t, "PRETTY123", repo.prettySeen)
}

func TestLookupReturnsRawErrorWhenBothLookupsMiss(t *testing.T) {
	rawErr := errors.New("raw not found")
	repo := &mockLookupRepo{
		getByTokenFunc: func(token string) (*models.InvitationAccessToken, error) {
			return nil, rawErr
		},
		getByPrettyTokenFunc: func(code string) (*models.InvitationAccessToken, error) {
			return nil, nil
		},
	}

	token, err := Lookup(repo, "missing")

	assert.Nil(t, token)
	assert.ErrorIs(t, err, rawErr)
}

func TestLookupIgnoresEmptyInput(t *testing.T) {
	token, err := Lookup(&mockLookupRepo{}, "   ")

	require.NoError(t, err)
	assert.Nil(t, token)
}
