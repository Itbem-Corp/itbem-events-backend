package fonts

import (
	"context"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

const legacyFontSetsCacheResource = "font_sets"

func ListFontSets() ([]models.FontSet, error) {
	if _fontSvc == nil {
		return nil, fontServiceUnavailable()
	}
	return _fontSvc.ListFontSets()
}

func GetFontSetByID(id uuid.UUID) (*models.FontSet, error) {
	if _fontSvc == nil {
		return nil, fontServiceUnavailable()
	}
	return _fontSvc.GetFontSetByID(id)
}

func CreateFontSet(obj *models.FontSet) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.CreateFontSet(obj)
}

func UpdateFontSet(obj *models.FontSet) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.UpdateFontSet(obj)
}

func DeleteFontSet(id uuid.UUID) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.DeleteFontSet(id)
}

func (fs *FontService) ListFontSets() ([]models.FontSet, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		fs.cache,
		"all:"+utils.RedisFontSetKey,
		utils.CacheTTLs[utils.RedisFontSetKey],
		func() ([]models.FontSet, error) {
			return fs.repo.ListFontSets(1, 0, "")
		},
	)
}

func (fs *FontService) invalidateFontSetCache() error {
	if fs.cache == nil {
		return nil
	}
	if err := fs.cache.Invalidate(legacyFontSetsCacheResource, "all"); err != nil {
		return err
	}
	return fs.cache.Invalidate(utils.RedisFontSetKey, "all")
}

func (fs *FontService) GetFontSetByID(id uuid.UUID) (*models.FontSet, error) {
	return fs.repo.GetFontSetByID(id)
}

func (fs *FontService) CreateFontSet(obj *models.FontSet) error {
	if err := fs.repo.CreateFontSet(obj); err != nil {
		return err
	}
	return fs.invalidateFontSetCache()
}

func (fs *FontService) UpdateFontSet(obj *models.FontSet) error {
	if err := fs.repo.UpdateFontSet(obj); err != nil {
		return err
	}
	return fs.invalidateFontSetCache()
}

func (fs *FontService) DeleteFontSet(id uuid.UUID) error {
	if err := fs.repo.DeleteFontSet(id); err != nil {
		return err
	}
	return fs.invalidateFontSetCache()
}
