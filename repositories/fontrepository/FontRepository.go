package fontrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

const RedisServiceFontsKey = "fonts"

func GetFontByID(id uuid.UUID) (*models.Font, error) {
	var font models.Font
	err := gormrepository.GetByID(&font, id)
	return &font, err
}

func CreateFont(font *models.Font) error {
	err := gormrepository.Insert(font)
	if err != nil {
		return ValidateError(err)
	}
	return nil
}

func UpdateFont(font *models.Font) error {
	return gormrepository.Update(font, font.ID)
}

func DeleteFont(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Font{})
}

func ListFonts(page int, pageSize int, name string) ([]models.Font, error) {
	var fonts []models.Font

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

	err := gormrepository.GetList(&fonts, opts)
	return fonts, err
}

func CreateMultipleFonts(fonts []models.Font) error {
	return gormrepository.InsertManyBatch(fonts, 10)
}
