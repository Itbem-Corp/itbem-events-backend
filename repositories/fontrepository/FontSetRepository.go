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
	err := gormrepository.GetByID(&fontSet, id, "Patterns.Font", "Patterns.Font.Resource")
	return &fontSet, err
}

func CreateFontSet(fontSet *models.FontSet) error {
	err := gormrepository.Insert(fontSet)
	if err != nil {
		return ValidateError(err)
	}
	if delErr := redisrepository.DeleteKeysByPattern(context.Background(), fontSetCachePattern()); delErr != nil {
		return delErr
	}
	return nil
}

func UpdateFontSet(fontSet *models.FontSet) error {
	err := gormrepository.Update(fontSet, fontSet.ID)
	if err == nil {
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), fontSetCachePattern()); delErr != nil {
			return delErr
		}
	}
	return err
}

func DeleteFontSet(id uuid.UUID) error {
	err := gormrepository.Delete(id, &models.FontSet{})
	if err == nil {
		if delErr := redisrepository.DeleteKeysByPattern(context.Background(), fontSetCachePattern()); delErr != nil {
			return delErr
		}
	}
	return err
}

func fontSetCachePattern() string {
	return "*" + utils.RedisFontSetKey + "*"
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
		Preload:  []string{"Patterns.Font", "Patterns.Font.Resource"},
	}

	if pageSize > 0 {
		opts.Limit = pageSize
		opts.Offset = (page - 1) * pageSize
	}

	err := gormrepository.GetList(&fontSet, opts)
	return fontSet, err
}

func (r *FontRepo) GetFontSetByID(id uuid.UUID) (*models.FontSet, error) {
	return GetFontSetByID(id)
}
func (r *FontRepo) CreateFontSet(fontSet *models.FontSet) error {
	return CreateFontSet(fontSet)
}
func (r *FontRepo) UpdateFontSet(fontSet *models.FontSet) error {
	return UpdateFontSet(fontSet)
}
func (r *FontRepo) DeleteFontSet(id uuid.UUID) error { return DeleteFontSet(id) }
func (r *FontRepo) ListFontSets(page int, pageSize int, name string) ([]models.FontSet, error) {
	return ListFontSets(page, pageSize, name)
}
