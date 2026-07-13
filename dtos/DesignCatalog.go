package dtos

import (
	"events-stocks/models"
	"sort"
	"time"

	"github.com/gofrs/uuid"
)

type ColorResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	HexCode   string    `json:"hex_code"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ColorPalettePatternResponse struct {
	ID             uuid.UUID      `json:"id"`
	ColorPaletteID uuid.UUID      `json:"color_palette_id"`
	ColorID        uuid.UUID      `json:"color_id"`
	Key            string         `json:"key"`
	Role           string         `json:"role"`
	Order          int            `json:"order"`
	Color          *ColorResponse `json:"color,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type ColorPaletteResponse struct {
	ID        uuid.UUID                     `json:"id"`
	Name      string                        `json:"name"`
	IsPremium bool                          `json:"is_premium"`
	Patterns  []ColorPalettePatternResponse `json:"patterns"`
	CreatedAt time.Time                     `json:"created_at"`
	UpdatedAt time.Time                     `json:"updated_at"`
}

type FontResponse struct {
	ID               uuid.UUID  `json:"id"`
	ResourceID       uuid.UUID  `json:"resource_id"`
	Name             string     `json:"name"`
	Family           string     `json:"family"`
	URL              string     `json:"url,omitempty"`
	ViewURL          string     `json:"view_url,omitempty"`
	ViewURLExpiresAt *time.Time `json:"view_url_expires_at,omitempty"`
	IsSerif          bool       `json:"is_serif"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type FontSetPatternResponse struct {
	ID        uuid.UUID     `json:"id"`
	FontSetID uuid.UUID     `json:"font_set_id"`
	FontID    uuid.UUID     `json:"font_id"`
	Key       string        `json:"key"`
	Role      string        `json:"role"`
	Order     int           `json:"order"`
	Font      *FontResponse `json:"font,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type FontSetResponse struct {
	ID        uuid.UUID                `json:"id"`
	Name      string                   `json:"name"`
	Patterns  []FontSetPatternResponse `json:"patterns"`
	CreatedAt time.Time                `json:"created_at"`
	UpdatedAt time.Time                `json:"updated_at"`
}

type DesignTemplateResponse struct {
	ID                      uuid.UUID             `json:"id"`
	Name                    string                `json:"name"`
	Identifier              string                `json:"identifier"`
	Description             string                `json:"description"`
	PreviewURL              string                `json:"preview_url"`
	PreviewImageURL         string                `json:"preview_image_url"`
	PreviewViewURL          string                `json:"preview_view_url,omitempty"`
	PreviewViewURLExpiresAt *time.Time            `json:"preview_view_url_expires_at,omitempty"`
	ColorPaletteID          *uuid.UUID            `json:"color_palette_id"`
	ColorPalette            *ColorPaletteResponse `json:"color_palette,omitempty"`
	DefaultColorPaletteID   *uuid.UUID            `json:"default_color_palette_id"`
	DefaultColorPalette     *ColorPaletteResponse `json:"default_color_palette,omitempty"`
	FontSetID               *uuid.UUID            `json:"font_set_id"`
	FontSet                 *FontSetResponse      `json:"font_set,omitempty"`
	DefaultFontSetID        *uuid.UUID            `json:"default_font_set_id"`
	DefaultFontSet          *FontSetResponse      `json:"default_font_set,omitempty"`
	AnimationsEnabled       bool                  `json:"animations_enabled"`
	HasDarkMode             bool                  `json:"has_dark_mode"`
	Category                string                `json:"category"`
	IsPremium               bool                  `json:"is_premium"`
	IsActive                bool                  `json:"is_active"`
	CreatedAt               time.Time             `json:"created_at"`
	UpdatedAt               time.Time             `json:"updated_at"`
}

type DesignCatalogWorkspaceResponse struct {
	Templates []DesignTemplateResponse `json:"templates"`
	Palettes  []ColorPaletteResponse   `json:"palettes"`
	FontSets  []FontSetResponse        `json:"font_sets"`
}

func NewColorResponse(color models.Color) ColorResponse {
	return ColorResponse{
		ID:        color.ID,
		Name:      color.Name,
		Value:     color.Value,
		HexCode:   color.Value,
		CreatedAt: color.CreatedAt,
		UpdatedAt: color.UpdatedAt,
	}
}

func colorResponsePtr(color models.Color) *ColorResponse {
	if color.ID == uuid.Nil {
		return nil
	}
	response := NewColorResponse(color)
	return &response
}

func NewColorPalettePatternResponse(pattern models.ColorPalettePattern) ColorPalettePatternResponse {
	return ColorPalettePatternResponse{
		ID:             pattern.ID,
		ColorPaletteID: pattern.ColorPaletteID,
		ColorID:        pattern.ColorID,
		Key:            pattern.Key,
		Role:           pattern.Key,
		Order:          pattern.Order,
		Color:          colorResponsePtr(pattern.Color),
		CreatedAt:      pattern.CreatedAt,
		UpdatedAt:      pattern.UpdatedAt,
	}
}

func NewColorPaletteResponse(palette *models.ColorPalette) ColorPaletteResponse {
	if palette == nil {
		return ColorPaletteResponse{Patterns: []ColorPalettePatternResponse{}}
	}

	patterns := make([]ColorPalettePatternResponse, 0, len(palette.Patterns))
	for _, pattern := range palette.Patterns {
		patterns = append(patterns, NewColorPalettePatternResponse(pattern))
	}
	sort.SliceStable(patterns, func(i, j int) bool {
		if patterns[i].Order == patterns[j].Order {
			return patterns[i].Key < patterns[j].Key
		}
		return patterns[i].Order < patterns[j].Order
	})

	return ColorPaletteResponse{
		ID:        palette.ID,
		Name:      palette.Name,
		IsPremium: false,
		Patterns:  patterns,
		CreatedAt: palette.CreatedAt,
		UpdatedAt: palette.UpdatedAt,
	}
}

func colorPaletteResponsePtr(palette *models.ColorPalette) *ColorPaletteResponse {
	if palette == nil {
		return nil
	}
	response := NewColorPaletteResponse(palette)
	return &response
}

func NewColorPaletteResponses(palettes []models.ColorPalette) []ColorPaletteResponse {
	response := make([]ColorPaletteResponse, 0, len(palettes))
	for i := range palettes {
		response = append(response, NewColorPaletteResponse(&palettes[i]))
	}
	return response
}

func NewFontResponse(font models.Font) FontResponse {
	return FontResponse{
		ID:         font.ID,
		ResourceID: font.ResourceID,
		Name:       font.Name,
		Family:     font.Name,
		URL:        font.Resource.Path,
		IsSerif:    font.IsSerif,
		CreatedAt:  font.CreatedAt,
		UpdatedAt:  font.UpdatedAt,
	}
}

func NewFontResponses(fonts []*models.Font) []FontResponse {
	response := make([]FontResponse, 0, len(fonts))
	for _, font := range fonts {
		if font == nil {
			continue
		}
		response = append(response, NewFontResponse(*font))
	}
	return response
}

func fontResponsePtr(font models.Font) *FontResponse {
	if font.ID == uuid.Nil {
		return nil
	}
	response := NewFontResponse(font)
	return &response
}

func NewFontSetPatternResponse(pattern models.FontSetPattern) FontSetPatternResponse {
	return FontSetPatternResponse{
		ID:        pattern.ID,
		FontSetID: pattern.FontSetID,
		FontID:    pattern.FontID,
		Key:       pattern.Key,
		Role:      pattern.Key,
		Order:     pattern.Order,
		Font:      fontResponsePtr(pattern.Font),
		CreatedAt: pattern.CreatedAt,
		UpdatedAt: pattern.UpdatedAt,
	}
}

func NewFontSetResponse(fontSet *models.FontSet) FontSetResponse {
	if fontSet == nil {
		return FontSetResponse{Patterns: []FontSetPatternResponse{}}
	}

	patterns := make([]FontSetPatternResponse, 0, len(fontSet.Patterns))
	for _, pattern := range fontSet.Patterns {
		patterns = append(patterns, NewFontSetPatternResponse(pattern))
	}
	sort.SliceStable(patterns, func(i, j int) bool {
		if patterns[i].Order == patterns[j].Order {
			return patterns[i].Key < patterns[j].Key
		}
		return patterns[i].Order < patterns[j].Order
	})

	return FontSetResponse{
		ID:        fontSet.ID,
		Name:      fontSet.Name,
		Patterns:  patterns,
		CreatedAt: fontSet.CreatedAt,
		UpdatedAt: fontSet.UpdatedAt,
	}
}

func fontSetResponsePtr(fontSet *models.FontSet) *FontSetResponse {
	if fontSet == nil {
		return nil
	}
	response := NewFontSetResponse(fontSet)
	return &response
}

func NewFontSetResponses(fontSets []models.FontSet) []FontSetResponse {
	response := make([]FontSetResponse, 0, len(fontSets))
	for i := range fontSets {
		response = append(response, NewFontSetResponse(&fontSets[i]))
	}
	return response
}

func NewDesignTemplateResponse(template *models.DesignTemplate) DesignTemplateResponse {
	if template == nil {
		return DesignTemplateResponse{}
	}

	palette := colorPaletteResponsePtr(template.ColorPalette)
	fontSet := fontSetResponsePtr(template.FontSet)

	return DesignTemplateResponse{
		ID:                    template.ID,
		Name:                  template.Name,
		Identifier:            template.Identifier,
		Description:           template.Description,
		PreviewURL:            template.PreviewURL,
		PreviewImageURL:       template.PreviewURL,
		PreviewViewURL:        template.PreviewURL,
		ColorPaletteID:        template.ColorPaletteID,
		ColorPalette:          palette,
		DefaultColorPaletteID: template.ColorPaletteID,
		DefaultColorPalette:   palette,
		FontSetID:             template.FontSetID,
		FontSet:               fontSet,
		DefaultFontSetID:      template.FontSetID,
		DefaultFontSet:        fontSet,
		AnimationsEnabled:     template.AnimationsEnabled,
		HasDarkMode:           template.HasDarkMode,
		Category:              template.Category,
		IsPremium:             template.IsPremium,
		IsActive:              template.IsActive,
		CreatedAt:             template.CreatedAt,
		UpdatedAt:             template.UpdatedAt,
	}
}

func designTemplateResponsePtr(template *models.DesignTemplate) *DesignTemplateResponse {
	if template == nil {
		return nil
	}
	response := NewDesignTemplateResponse(template)
	return &response
}

func NewDesignTemplateResponses(templates []models.DesignTemplate) []DesignTemplateResponse {
	response := make([]DesignTemplateResponse, 0, len(templates))
	for i := range templates {
		response = append(response, NewDesignTemplateResponse(&templates[i]))
	}
	return response
}
