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

func TestNewResourceTypeResponseUsesCatalogContract(t *testing.T) {
	now := time.Date(2026, time.July, 7, 13, 0, 0, 0, time.UTC)

	body := NewResourceTypeResponse(models.ResourceType{
		ID:        uuid.Must(uuid.NewV4()),
		Code:      "image",
		Label:     "Imagen",
		CreatedAt: now,
		UpdatedAt: now.Add(time.Minute),
	})

	assert.Equal(t, "image", body.Code)
	assert.Equal(t, "Imagen", body.Label)
	assert.Equal(t, now, body.CreatedAt)

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"code":"image"`)
	assert.Contains(t, string(raw), `"label":"Imagen"`)
}

func TestNewResourceTypeResponsesReturnsEmptyArray(t *testing.T) {
	assert.Empty(t, NewResourceTypeResponses(nil))
}
