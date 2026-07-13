package fontrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

const RedisServiceFontsKey = "fonts"

func GetFontByID(id uuid.UUID) (*models.Font, error) {
	var font models.Font
	err := gormrepository.GetByID(&font, id, "Resource")
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
		Preload:  []string{"Resource"},
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

// FontRepo implements ports.FontRepository.
type FontRepo struct{}

func NewFontRepo() *FontRepo { return &FontRepo{} }

func (r *FontRepo) GetFontByID(id uuid.UUID) (*models.Font, error) {
	return GetFontByID(id)
}
func (r *FontRepo) CreateFont(font *models.Font) error { return CreateFont(font) }
func (r *FontRepo) UpdateFont(font *models.Font) error { return UpdateFont(font) }
func (r *FontRepo) DeleteFont(id uuid.UUID) error      { return DeleteFont(id) }
func (r *FontRepo) ListFonts(page int, pageSize int, name string) ([]models.Font, error) {
	return ListFonts(page, pageSize, name)
}
func (r *FontRepo) CreateMultipleFonts(fonts []models.Font) error {
	return CreateMultipleFonts(fonts)
}
