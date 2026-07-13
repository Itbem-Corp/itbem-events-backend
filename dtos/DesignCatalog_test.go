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

func TestNewDesignTemplateResponseIncludesDashboardAliases(t *testing.T) {
	templateID := uuid.Must(uuid.NewV4())
	paletteID := uuid.Must(uuid.NewV4())
	colorPatternID := uuid.Must(uuid.NewV4())
	colorID := uuid.Must(uuid.NewV4())
	fontSetID := uuid.Must(uuid.NewV4())
	fontPatternID := uuid.Must(uuid.NewV4())
	fontID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	createdAt := time.Date(2026, 7, 7, 18, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)

	response := NewDesignTemplateResponse(&models.DesignTemplate{
		ID:                templateID,
		Name:              "Moderna",
		Identifier:        "modern",
		Description:       "Template limpio",
		PreviewURL:        "https://cdn.example.com/modern.webp",
		ColorPaletteID:    &paletteID,
		FontSetID:         &fontSetID,
		AnimationsEnabled: true,
		HasDarkMode:       true,
		Category:          "minimal",
		IsPremium:         true,
		IsActive:          true,
		CreatedAt:         createdAt,
		UpdatedAt:         updatedAt,
		ColorPalette: &models.ColorPalette{
			ID:        paletteID,
			Name:      "Dorada",
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			Patterns: []models.ColorPalettePattern{
				{
					ID:             colorPatternID,
					ColorPaletteID: paletteID,
					ColorID:        colorID,
					Key:            "PRIMARY",
					Order:          1,
					CreatedAt:      createdAt,
					UpdatedAt:      updatedAt,
					Color: models.Color{
						ID:        colorID,
						Name:      "Oro",
						Value:     "#c8a45d",
						CreatedAt: createdAt,
						UpdatedAt: updatedAt,
					},
				},
			},
		},
		FontSet: &models.FontSet{
			ID:        fontSetID,
			Name:      "Editorial",
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			Patterns: []models.FontSetPattern{
				{
					ID:        fontPatternID,
					FontSetID: fontSetID,
					FontID:    fontID,
					Key:       "HEADING",
					Order:     1,
					CreatedAt: createdAt,
					UpdatedAt: updatedAt,
					Font: models.Font{
						ID:         fontID,
						ResourceID: resourceID,
						Resource: models.Resource{
							ID:   resourceID,
							Path: "base/fonts/cormorant.woff2",
						},
						Name:      "Cormorant Garamond",
						IsSerif:   true,
						CreatedAt: createdAt,
						UpdatedAt: updatedAt,
					},
				},
			},
		},
	})

	require.NotNil(t, response.DefaultColorPalette)
	require.NotNil(t, response.DefaultFontSet)
	require.Len(t, response.DefaultColorPalette.Patterns, 1)
	require.Len(t, response.DefaultFontSet.Patterns, 1)

	assert.Equal(t, response.PreviewURL, response.PreviewImageURL)
	assert.Equal(t, response.PreviewURL, response.PreviewViewURL)
	assert.Nil(t, response.PreviewViewURLExpiresAt)
	assert.Equal(t, response.ColorPaletteID, response.DefaultColorPaletteID)
	assert.Equal(t, response.ColorPalette, response.DefaultColorPalette)
	assert.Equal(t, response.FontSetID, response.DefaultFontSetID)
	assert.Equal(t, response.FontSet, response.DefaultFontSet)
	assert.Equal(t, "PRIMARY", response.DefaultColorPalette.Patterns[0].Role)
	assert.Equal(t, "#c8a45d", response.DefaultColorPalette.Patterns[0].Color.HexCode)
	assert.Equal(t, "HEADING", response.DefaultFontSet.Patterns[0].Role)
	assert.Equal(t, "Cormorant Garamond", response.DefaultFontSet.Patterns[0].Font.Family)
	assert.Equal(t, "base/fonts/cormorant.woff2", response.DefaultFontSet.Patterns[0].Font.URL)
	assert.True(t, response.DefaultFontSet.Patterns[0].Font.IsSerif)

	encoded, err := json.Marshal(response)
	require.NoError(t, err)

	assert.Contains(t, string(encoded), `"preview_image_url"`)
	assert.Contains(t, string(encoded), `"preview_view_url"`)
	assert.Contains(t, string(encoded), `"default_color_palette"`)
	assert.Contains(t, string(encoded), `"default_font_set"`)
	assert.Contains(t, string(encoded), `"hex_code"`)
	assert.Contains(t, string(encoded), `"family"`)
	assert.Contains(t, string(encoded), `"url":"base/fonts/cormorant.woff2"`)
	assert.NotContains(t, string(encoded), "DeletedAt")
	assert.NotContains(t, string(encoded), "deleted_at")
}

func TestNewCatalogResponsesReturnEmptyArrays(t *testing.T) {
	assert.Empty(t, NewDesignTemplateResponses(nil))
	assert.Empty(t, NewColorPaletteResponses(nil))
	assert.Empty(t, NewFontSetResponses(nil))
	assert.Empty(t, NewFontResponses(nil))

	palette := NewColorPaletteResponse(&models.ColorPalette{})
	fontSet := NewFontSetResponse(&models.FontSet{})

	require.NotNil(t, palette.Patterns)
	require.NotNil(t, fontSet.Patterns)
	assert.Len(t, palette.Patterns, 0)
	assert.Len(t, fontSet.Patterns, 0)
}

func TestNewFontResponsesUsesUploadContract(t *testing.T) {
	now := time.Date(2026, 7, 7, 19, 0, 0, 0, time.UTC)
	resourceID := uuid.Must(uuid.NewV4())
	fontID := uuid.Must(uuid.NewV4())

	body := NewFontResponses([]*models.Font{
		{
			ID:         fontID,
			ResourceID: resourceID,
			Resource: models.Resource{
				ID:    resourceID,
				Path:  "base/fonts/editorial.woff2",
				Title: "Should not leak as resource",
			},
			Name:      "Editorial",
			IsSerif:   true,
			CreatedAt: now,
			UpdatedAt: now.Add(time.Minute),
		},
		nil,
	})

	require.Len(t, body, 1)
	assert.Equal(t, fontID, body[0].ID)
	assert.Equal(t, resourceID, body[0].ResourceID)
	assert.Equal(t, "Editorial", body[0].Name)
	assert.Equal(t, "Editorial", body[0].Family)
	assert.Equal(t, "base/fonts/editorial.woff2", body[0].URL)
	assert.True(t, body[0].IsSerif)

	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "resource\":")
	assert.NotContains(t, string(encoded), "Should not leak as resource")
}
