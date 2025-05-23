package fontrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func GetFontPatternByID(id uuid.UUID) (*models.FontSetPattern, error) {
	var pattern models.FontSetPattern
	err := gormrepository.GetByID(&pattern, id, "Font")
	return &pattern, err
}

func CreateFontPattern(pattern *models.FontSetPattern) error {
	err := gormrepository.Insert(pattern)
	if err != nil {
		return ValidateError(err)
	}
	return nil
}

func UpdateFontPattern(pattern *models.FontSetPattern) error {
	return gormrepository.Update(pattern, pattern.ID)
}

func DeleteFontPattern(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.FontSetPattern{})
}

func ListFontPatterns(fontSetID *uuid.UUID) ([]models.FontSetPattern, error) {
	var patterns []models.FontSetPattern
	filters := map[string]interface{}{
		"font_set_id": fontSetID,
	}

	opts := gormrepository.QueryOptions{
		Filters:  filters,
		OrderBy:  "order",
		OrderDir: "asc",
	}

	err := gormrepository.GetList(&patterns, opts)
	return patterns, err
}
