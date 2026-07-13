package fonts

import (
	"context"
	"fmt"
	"mime/multipart"
	"strings"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	resources "events-stocks/services/resources"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

var _fontSvc *FontService

func SetDefaultFontService(svc *FontService) { _fontSvc = svc }

func fontServiceUnavailable() error {
	return fmt.Errorf("font service not initialized")
}

func ListFontCollection() ([]models.Font, error) {
	if _fontSvc == nil {
		return nil, fontServiceUnavailable()
	}
	return _fontSvc.ListFontCollection()
}

func GetFontByID(id uuid.UUID) (*models.Font, error) {
	if _fontSvc == nil {
		return nil, fontServiceUnavailable()
	}
	return _fontSvc.GetFontByID(id)
}

func CreateFont(obj *models.Font) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.CreateFont(obj)
}

func UpdateFont(obj *models.Font) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.UpdateFont(obj)
}

func DeleteFont(id uuid.UUID) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.DeleteFont(id)
}

type FontService struct {
	ResourceSvc *resources.ResourceService
	repo        ports.FontRepository
	cache       ports.CacheRepository
}

type FontServiceDeps struct {
	Repo  ports.FontRepository
	Cache ports.CacheRepository
}

func NewFontService(rs *resources.ResourceService, deps ...FontServiceDeps) *FontService {
	var dep FontServiceDeps
	if len(deps) > 0 {
		dep = deps[0]
	}
	return &FontService{
		ResourceSvc: rs,
		repo:        dep.Repo,
		cache:       dep.Cache,
	}
}

func (fs *FontService) ListFontCollection() ([]models.Font, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		fs.cache,
		"all:"+utils.RedisFontsKey,
		utils.CacheTTLs[utils.RedisFontsKey],
		func() ([]models.Font, error) {
			return fs.repo.ListFonts(1, 0, "")
		},
	)
}

func (fs *FontService) GetFontByID(id uuid.UUID) (*models.Font, error) {
	return fs.repo.GetFontByID(id)
}

func (fs *FontService) CreateFont(obj *models.Font) error {
	if err := fs.repo.CreateFont(obj); err != nil {
		return err
	}
	return fs.invalidateFontCatalogCaches()
}

func (fs *FontService) UpdateFont(obj *models.Font) error {
	if err := fs.repo.UpdateFont(obj); err != nil {
		return err
	}
	return fs.invalidateFontCatalogCaches()
}

func (fs *FontService) DeleteFont(id uuid.UUID) error {
	if err := fs.repo.DeleteFont(id); err != nil {
		return err
	}
	return fs.invalidateFontCatalogCaches()
}

func (fs *FontService) invalidateFontCache() error {
	if fs.cache == nil {
		return nil
	}
	return fs.cache.Invalidate(utils.RedisFontsKey, "all")
}

func (fs *FontService) invalidateFontCatalogCaches() error {
	if err := fs.invalidateFontCache(); err != nil {
		return err
	}
	return fs.invalidateFontSetCache()
}

func (fs *FontService) UploadAndCreateFonts(
	files []*multipart.FileHeader,
) ([]*models.Font, error) {
	if fs == nil || fs.ResourceSvc == nil {
		return nil, fmt.Errorf("resource service not configured")
	}
	if fs.repo == nil {
		return nil, fmt.Errorf("font repository not configured")
	}

	subfolder := "base/fonts"
	resourceTypeCode := "font"
	uploadedResources, err := fs.ResourceSvc.UploadBaseResources(files, subfolder, resourceTypeCode)
	if err != nil {
		return nil, err
	}

	var fonts []models.Font
	for _, res := range uploadedResources {
		cleanName := res.Title
		if dot := strings.LastIndex(cleanName, "."); dot != -1 {
			cleanName = cleanName[:dot]
		}

		fonts = append(fonts, models.Font{
			Name:       cleanName,
			ResourceID: res.ID,
		})
	}

	if err := fs.repo.CreateMultipleFonts(fonts); err != nil {
		fs.ResourceSvc.RollbackUploadedResources(uploadedResources)
		return nil, err
	}
	for i := range fonts {
		if i < len(uploadedResources) && uploadedResources[i] != nil {
			fonts[i].Resource = *uploadedResources[i]
		}
	}
	if fs.cache != nil {
		_ = fs.invalidateFontCatalogCaches()
	}

	result := make([]*models.Font, 0, len(fonts))
	for i := range fonts {
		result = append(result, &fonts[i])
	}

	return result, nil
}
