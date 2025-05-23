package colors

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/colorrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListColorPalettePatterns() ([]models.ColorPalettePattern, error) {
	cacheKey := "all:colors"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.ColorPalettePattern
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := colorrepository.ListPatternsByPalette()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["colors"])

	return data, nil
}

func GetColorPalettePatternByID(id uuid.UUID) (*models.ColorPalettePattern, error) {
	return colorrepository.GetColorPatternByID(id)
}

func CreateColorPalettePattern(obj *models.ColorPalettePattern) error {
	if err := colorrepository.CreatePattern(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func UpdateColorPalettePattern(obj *models.ColorPalettePattern) error {
	if err := colorrepository.UpdatePattern(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func DeleteColorPalettePattern(id uuid.UUID) error {
	if err := colorrepository.DeletePattern(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}
