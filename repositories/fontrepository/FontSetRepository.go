package fontrepository

import (
	"context"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func GetFontSetByID(id uuid.UUID) (*models.FontSet, error) {
	var fontSet models.FontSet
	err := gormrepository.GetByID(&fontSet, id, "Patterns.Font")
	return &fontSet, err
}

func CreateFontSet(fontSet *models.FontSet) error {
	err := gormrepository.Insert(fontSet)
	if err != nil {
		return ValidateError(err)
	}
	pattern := "*" + utils.RedisFontSetKey + "*"
	if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
		return delErr
	}
	return nil
}

func UpdateFontSet(fontSet *models.FontSet) error {
	err := gormrepository.Update(fontSet, fontSet.ID)
	if err == nil {
		pattern := "*" + utils.RedisFontSetKey + "*"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func DeleteFontSet(id uuid.UUID) error {
	err := gormrepository.Delete(id, &models.FontSet{})
	if err == nil {
		pattern := "*" + utils.RedisFontSetKey + "*"
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), pattern); delErr != nil {
			return delErr
		}
	}
	return err
}

func ListFontSets(page int, pageSize int, name string) ([]models.FontSet, error) {
	var fontSet []models.FontSet

	filters := map[string]interface{}{}
	if name != "" {
		filters["name"] = name
	}

	opts := gormrepository.QueryOptions{
		Filters:  filters,
		OrderBy:  "id",
		OrderDir: "desc",
	}

	if pageSize > 0 {
		opts.Limit = pageSize
		opts.Offset = (page - 1) * pageSize
	}

	err := gormrepository.GetList(&fontSet, opts)
	return fontSet, err
}
