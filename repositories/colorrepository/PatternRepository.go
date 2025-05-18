package colorrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

const RedisPatternColorsServiceKey = "colors"

func GetColorPatternByID(id uuid.UUID) (*models.ColorPalettePattern, error) {
	var colorPatternPalette models.ColorPalettePattern
	err := gormrepository.GetByID(&colorPatternPalette, id)
	return &colorPatternPalette, err
}

func CreatePattern(pattern *models.ColorPalettePattern) error {
	return gormrepository.Insert(pattern)
}

func UpdatePattern(pattern *models.ColorPalettePattern) error {
	return gormrepository.Update(pattern, pattern.ID)
}

func DeletePattern(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.ColorPalettePattern{})
}

func ListPatternsByPalette(paletteID uuid.UUID) ([]models.ColorPalettePattern, error) {
	var patterns []models.ColorPalettePattern
	err := gormrepository.GetList(&patterns, gormrepository.QueryOptions{
		Filters: map[string]interface{}{"color_palette_id": paletteID},
		Preload: []string{"Color"},
		OrderBy: "order",
	})
	return patterns, err
}
