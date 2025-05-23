package fonts

import (
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/cacheloaderrepository"
	"events-stocks/repositories/fontrepository"
	"events-stocks/repositories/redisrepository"
	services "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"mime/multipart"
	"strings"
)

type FontService struct {
	ResourceSvc *services.ResourceService
}

func NewFontService(rs *services.ResourceService) *FontService {
	return &FontService{ResourceSvc: rs}
}

func ListFontCollection() ([]models.Font, error) {
	jsonStr, err := cacheloaderrepository.CacheOrLoad(
		utils.RedisFontsKey,
		"all",
		utils.CacheTTLs[utils.RedisFontsKey],
		func() (string, error) {
			data, err := fontrepository.ListFonts(1, 0, "")
			if err != nil {
				return "", err
			}
			return utils.MarshallData(data, nil)
		},
	)

	if err != nil {
		return fontrepository.ListFonts(1, 0, "")
	}

	var result []models.Font
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return fontrepository.ListFonts(1, 0, "")
	}

	return result, nil
}

func GetFontByID(id uuid.UUID) (*models.Font, error) {
	return fontrepository.GetFontByID(id)
}

func CreateFont(obj *models.Font) error {
	if err := fontrepository.CreateFont(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func UpdateFont(obj *models.Font) error {
	if err := fontrepository.UpdateFont(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func DeleteFont(id uuid.UUID) error {
	if err := fontrepository.DeleteFont(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("fonts", "all")
}

func (fs *FontService) UploadAndCreateFonts(
	files []*multipart.FileHeader,
) ([]*models.Font, error) {
	subfolder := "base/fonts"
	resourceTypeCode := "font" // usamos el Code, no el Name

	// ✅ Subir a S3 y crear los recursos
	uploadedResources, err := fs.ResourceSvc.UploadBaseResources(files, subfolder, resourceTypeCode)
	if err != nil {
		return nil, err
	}

	// 🧱 Crear modelos Font con los recursos ya subidos
	var fonts []models.Font
	for _, res := range uploadedResources {
		cleanName := res.Title
		if dot := strings.LastIndex(cleanName, "."); dot != -1 {
			cleanName = cleanName[:dot]
		}

		font := models.Font{
			Name:       cleanName,
			ResourceID: res.ID,
		}
		fonts = append(fonts, font)
	}

	// 💾 Insertar en DB
	if err := fontrepository.CreateMultipleFonts(fonts); err != nil {
		return nil, err
	}

	// 📦 Devolver en []*models.Font
	var result []*models.Font
	for i := range fonts {
		result = append(result, &fonts[i])
	}

	return result, nil
}
