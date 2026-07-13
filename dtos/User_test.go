package dtos

import (
	"encoding/json"
	"events-stocks/models"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUserProfileResponseHidesInternalIdentity(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	user := &models.User{
		ID:           userID,
		CognitoSub:   "internal-cognito-sub",
		Email:        "ana@example.com",
		FirstName:    "Ana",
		LastName:     "Lopez",
		ProfileImage: "avatars/raw-path.webp",
		IsActive:     true,
		IsRoot:       true,
		CreatedAt:    time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
	}

	body := NewUserProfileResponse(user, "https://signed.example.com/avatar.webp")
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.Equal(t, userID, body.ID)
	assert.Equal(t, "https://signed.example.com/avatar.webp", body.ProfileImage)
	assert.NotContains(t, string(encoded), "cognito_sub")
	assert.NotContains(t, string(encoded), "internal-cognito-sub")
	assert.NotContains(t, string(encoded), "deleted_at")
}

func TestAvatarResponseContract(t *testing.T) {
	encoded, err := json.Marshal(AvatarResponse{
		Path: "avatars/user-1.webp",
		URL:  "https://signed.example.com/avatars/user-1.webp",
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"path":"avatars/user-1.webp",
		"url":"https://signed.example.com/avatars/user-1.webp"
	}`, string(encoded))
}

func TestNewAdminUsersPageResponseUsesTypedRows(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC)

	body := NewAdminUsersPageResponse(
		[]models.User{{
			ID:         userID,
			CognitoSub: "hidden-sub",
			Email:      "root@example.com",
			FirstName:  "Root",
			LastName:   "User",
			IsActive:   true,
			IsRoot:     true,
			CreatedAt:  createdAt,
		}},
		map[uuid.UUID]int64{userID: 3},
		11,
		2,
		5,
	)

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	require.Len(t, body.Data, 1)
	assert.Equal(t, 11, body.Total)
	assert.Equal(t, 3, body.TotalPages)
	assert.Equal(t, int64(3), body.Data[0].Clients)
	assert.NotContains(t, string(encoded), "cognito_sub")
	assert.NotContains(t, string(encoded), "hidden-sub")
	assert.NotContains(t, string(encoded), "deleted_at")
}

func TestNewAdminUserDetailResponseNormalizesNilClients(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	user := &models.User{
		ID:         userID,
		CognitoSub: "hidden-sub",
		Email:      "ana@example.com",
		FirstName:  "Ana",
		LastName:   "Lopez",
		IsActive:   true,
		CreatedAt:  time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC),
	}

	body := NewAdminUserDetailResponse(user, nil)
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.Equal(t, userID, body.ID)
	assert.NotNil(t, body.Clients)
	assert.JSONEq(t, `[]`, string(mustMarshal(t, body.Clients)))
	assert.NotContains(t, string(encoded), "cognito_sub")
	assert.NotContains(t, string(encoded), "hidden-sub")
}

func mustMarshal(t *testing.T, value interface{}) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return encoded
}
