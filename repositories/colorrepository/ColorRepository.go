package colorrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

const RedisColorsServiceKey = "colors"

func GetColorByID(id uuid.UUID) (*models.Color, error) {
	var color models.Color
	err := gormrepository.GetByID(&color, id)
	return &color, err
}

func CreateColor(color *models.Color) error {
	return gormrepository.Insert(color)
}

func UpdateColor(color *models.Color) error {
	return gormrepository.Update(color, color.ID)
}

func DeleteColor(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Color{})
}

func ListColors() ([]models.Color, error) {
	var colors []models.Color
	err := gormrepository.GetList(&colors, gormrepository.QueryOptions{
		OrderBy:  "name",
		OrderDir: "asc",
	})
	return colors, err
}

func CreateMultipleColors(colors []models.Color) error {
	return gormrepository.InsertManyBatch(colors, 10)
}
