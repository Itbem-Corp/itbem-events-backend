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

func TestNewClientResponseHidesGormRelationsAndKeepsHierarchySummary(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	parentID := uuid.Must(uuid.NewV4())
	childID := uuid.Must(uuid.NewV4())
	typeID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)

	body := NewClientResponse(&models.Client{
		ID:           clientID,
		Name:         "Cliente Norte",
		Code:         "cliente-norte",
		ClientTypeID: typeID,
		ClientType: models.ClientType{
			ID:       typeID,
			Name:     "Cliente",
			Code:     "CUSTOMER",
			IsActive: true,
		},
		Logo:      "https://signed.example.com/logo.webp",
		IsActive:  true,
		ParentID:  &parentID,
		CreatedAt: createdAt,
		Parent: &models.Client{
			ID:   parentID,
			Name: "Agencia Madre",
			Code: "agencia-madre",
		},
		Children: []models.Client{{
			ID:   childID,
			Name: "Sucursal",
			Code: "sucursal",
		}},
		Members: []models.ClientMember{{
			User: models.User{CognitoSub: "internal-cognito-sub"},
		}},
	})

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.Equal(t, clientID, body.ID)
	require.NotNil(t, body.ClientType)
	assert.Equal(t, "CUSTOMER", body.ClientType.Code)
	require.NotNil(t, body.Parent)
	assert.Equal(t, parentID, body.Parent.ID)
	require.Len(t, body.Children, 1)
	assert.Equal(t, childID, body.Children[0].ID)
	assert.NotContains(t, string(encoded), "members")
	assert.NotContains(t, string(encoded), "cognito_sub")
	assert.NotContains(t, string(encoded), "internal-cognito-sub")
	assert.NotContains(t, string(encoded), "deleted_at")
}

func TestNewClientResponsesReturnsEmptyArray(t *testing.T) {
	body := NewClientResponses(nil)
	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	assert.JSONEq(t, `[]`, string(encoded))
}
