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

func TestNewClientMemberResponseKeepsDashboardContract(t *testing.T) {
	memberID := uuid.Must(uuid.NewV4())
	clientID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4())
	roleID := uuid.Must(uuid.NewV4())
	joinedAt := time.Date(2026, 7, 7, 15, 30, 0, 0, time.UTC)

	body := NewClientMemberResponse(models.ClientMember{
		ID:           memberID,
		ClientID:     clientID,
		UserID:       userID,
		ClientRoleID: roleID,
		CreatedAt:    joinedAt,
		User: models.User{
			ID:           userID,
			CognitoSub:   "internal-cognito-sub",
			FirstName:    "Ana",
			LastName:     "Lopez",
			Email:        "ana@example.com",
			ProfileImage: "avatars/ana.webp",
			IsActive:     true,
		},
		ClientRole: models.ClientRole{
			ID:   roleID,
			Code: "ADMIN",
			Name: "Administrador",
		},
	})

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.Equal(t, memberID, body.ID)
	assert.Equal(t, clientID, body.ClientID)
	assert.Equal(t, userID, body.UserID)
	assert.Equal(t, roleID, body.RoleID)
	assert.Equal(t, "ADMIN", body.RoleCode)
	assert.Equal(t, "ADMIN", body.Role)
	assert.Equal(t, "Administrador", body.RoleName)
	assert.Equal(t, "Ana", body.FirstName)
	assert.Equal(t, "Ana", body.User.FirstName)
	assert.Equal(t, joinedAt, body.JoinedAt)
	assert.NotContains(t, string(encoded), "cognito_sub")
	assert.NotContains(t, string(encoded), "internal-cognito-sub")
	assert.NotContains(t, string(encoded), "client_role")
	assert.NotContains(t, string(encoded), "deleted_at")
}

func TestClientMemberLinkResponseOmitsEmptyEmail(t *testing.T) {
	body := ClientMemberLinkResponse{
		UserID:   uuid.Must(uuid.NewV4()),
		ClientID: uuid.Must(uuid.NewV4()),
		RoleID:   uuid.Must(uuid.NewV4()),
	}

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.NotContains(t, string(encoded), "email")
}
