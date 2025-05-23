package fonts

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/fontrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListFontSets() ([]models.FontSet, error) {
	cacheKey := "all:fonts"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.FontSet
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := fontrepository.ListFontSets(1, 0, "")
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["fonts"])

	return data, nil
}

func GetFontSetByID(id uuid.UUID) (*models.FontSet, error) {
	return fontrepository.GetFontSetByID(id)
}

func CreateFontSet(obj *models.FontSet) error {
	if err := fontrepository.CreateFontSet(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func UpdateFontSet(obj *models.FontSet) error {
	if err := fontrepository.UpdateFontSet(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func DeleteFontSet(id uuid.UUID) error {
	if err := fontrepository.DeleteFontSet(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}
