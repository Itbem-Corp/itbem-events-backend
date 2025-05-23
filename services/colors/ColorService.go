package colors

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/cacheloaderrepository"
	"events-stocks/repositories/colorrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/repositories/resourcerepository"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
)

func ListColorCollection() ([]models.Color, error) {
	jsonStr, err := cacheloaderrepository.CacheOrLoad(
		utils.RedisColorsServiceKey,
		"all",
		utils.CacheTTLs[utils.RedisColorsServiceKey],
		func() (string, error) {
			data, err := colorrepository.ListColors()
			if err != nil {
				return "", err
			}
			return utils.MarshallData(data, nil)
		},
	)

	if err != nil {
		return colorrepository.ListColors()
	}

	var result []models.Color
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return colorrepository.ListColors()
	}

	return result, nil
}

func GetColorByID(id uuid.UUID) (*models.Color, error) {
	return colorrepository.GetColorByID(id)
}

func CreateColor(obj *models.Color) error {
	if err := colorrepository.CreateColor(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func UpdateColor(obj *models.Color) error {
	if err := colorrepository.UpdateColor(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func DeleteColor(id uuid.UUID) error {
	if err := colorrepository.DeleteColor(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("colors", "all")
}

func CreateMultipleColors(colors []models.Color) error {
	if len(colors) == 0 {
		return fmt.Errorf("no colors provided")
	}

	if err := colorrepository.CreateMultipleColors(colors); err != nil {
		return err
	}

	return redisrepository.Invalidate(utils.RedisPaletteServiceKey, "all")
}
