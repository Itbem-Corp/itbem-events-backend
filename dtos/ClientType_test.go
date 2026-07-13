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

func TestNewClientTypeResponseUsesSnakeCaseContract(t *testing.T) {
	typeID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)

	body := NewClientTypeResponse(models.ClientType{
		ID:          typeID,
		Name:        "Agencia",
		Code:        "AGENCY",
		Description: "Cliente organizador",
		Level:       2,
		IsActive:    true,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	})

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"id":"`+typeID.String()+`",
		"name":"Agencia",
		"code":"AGENCY",
		"description":"Cliente organizador",
		"level":2,
		"is_active":true,
		"created_at":"2026-07-07T18:00:00Z",
		"updated_at":"2026-07-07T19:00:00Z"
	}`, string(encoded))
	assert.NotContains(t, string(encoded), "ID")
	assert.NotContains(t, string(encoded), "CreatedAt")
	assert.NotContains(t, string(encoded), "UpdatedAt")
}
