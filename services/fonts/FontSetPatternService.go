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

func ListFontSetPatterns(id *uuid.UUID) ([]models.FontSetPattern, error) {
	cacheKey := "all:fonts"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.FontSetPattern
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := fontrepository.ListFontPatterns(id)
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["fonts"])

	return data, nil
}

func GetFontSetPatternByID(id uuid.UUID) (*models.FontSetPattern, error) {
	return fontrepository.GetFontPatternByID(id)
}

func CreateFontSetPattern(obj *models.FontSetPattern) error {
	if err := fontrepository.CreateFontPattern(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func UpdateFontSetPattern(obj *models.FontSetPattern) error {
	if err := fontrepository.UpdateFontPattern(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func DeleteFontSetPattern(id uuid.UUID) error {
	if err := fontrepository.DeleteFontPattern(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}
