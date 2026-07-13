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

func TestNewClientRoleResponseUsesSnakeCaseContract(t *testing.T) {
	roleID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 16, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)

	body := NewClientRoleResponse(models.ClientRole{
		ID:          roleID,
		Name:        "Administrador",
		Code:        "ADMIN",
		Description: "Puede administrar miembros",
		Hierarchy:   2,
		IsActive:    true,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	})

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"id":"`+roleID.String()+`",
		"name":"Administrador",
		"code":"ADMIN",
		"description":"Puede administrar miembros",
		"hierarchy":2,
		"is_active":true,
		"created_at":"2026-07-07T16:00:00Z",
		"updated_at":"2026-07-07T17:00:00Z"
	}`, string(encoded))
	assert.NotContains(t, string(encoded), "ID")
	assert.NotContains(t, string(encoded), "CreatedAt")
	assert.NotContains(t, string(encoded), "UpdatedAt")
}
