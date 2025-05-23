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

func ListColorPalettes() ([]models.ColorPalette, error) {
	cacheKey := "all:colors"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.ColorPalette
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := colorrepository.ListColorPalettes()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["colors"])

	return data, nil
}

func GetColorPaletteByID(id uuid.UUID) (*models.ColorPalette, error) {
	return colorrepository.GetColorPaletteByID(id)
}

func CreateColorPalette(obj *models.ColorPalette) error {
	if err := colorrepository.CreatePalette(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func UpdateColorPalette(obj *models.ColorPalette) error {
	if err := colorrepository.UpdatePalette(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func DeleteColorPalette(id uuid.UUID) error {
	if err := colorrepository.DeletePalette(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}
