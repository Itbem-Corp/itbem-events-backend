package colorrepository

import (
	"context"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func GetColorPaletteByID(id uuid.UUID) (*models.ColorPalette, error) {
	var colorPalette models.ColorPalette
	err := gormrepository.GetByID(&colorPalette, id)
	return &colorPalette, err
}

func CreatePalette(palette *models.ColorPalette) error {
	err := gormrepository.Insert(palette)
	if err != nil {
		return ValidateError(err)
	}

	pattern := "*" + utils.RedisPaletteServiceKey + "*"
	if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
		return delErr
	}

	return nil
}

func UpdatePalette(palette *models.ColorPalette) error {
	err := gormrepository.Update(palette, palette.ID)
	if err == nil {
		pattern := "*" + utils.RedisPaletteServiceKey + "*"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func DeletePalette(id uuid.UUID) error {
	err := gormrepository.Delete(id, &models.ColorPalette{})
	if err == nil {
		pattern := "*" + utils.RedisPaletteServiceKey + "*"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func ListColorPalettes() ([]models.ColorPalette, error) {
	var palettes []models.ColorPalette
	err := gormrepository.GetList(&palettes, gormrepository.QueryOptions{
		Preload:  []string{"Patterns.Color"},
		OrderBy:  "id",
		OrderDir: "asc",
	})
	return palettes, err
}
