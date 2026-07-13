package fonts

import (
	"context"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func fontSetPatternsCacheKey(id *uuid.UUID) string {
	if id == nil {
		return "all:font_set_patterns"
	}
	return fmt.Sprintf("font_set:%s:font_set_patterns", id.String())
}

func ListFontSetPatterns(id *uuid.UUID) ([]models.FontSetPattern, error) {
	if _fontSvc == nil {
		return nil, fontServiceUnavailable()
	}
	return _fontSvc.ListFontSetPatterns(id)
}

func GetFontSetPatternByID(id uuid.UUID) (*models.FontSetPattern, error) {
	if _fontSvc == nil {
		return nil, fontServiceUnavailable()
	}
	return _fontSvc.GetFontSetPatternByID(id)
}

func CreateFontSetPattern(obj *models.FontSetPattern) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.CreateFontSetPattern(obj)
}

func UpdateFontSetPattern(obj *models.FontSetPattern) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.UpdateFontSetPattern(obj)
}

func DeleteFontSetPattern(id uuid.UUID) error {
	if _fontSvc == nil {
		return fontServiceUnavailable()
	}
	return _fontSvc.DeleteFontSetPattern(id)
}

func (fs *FontService) ListFontSetPatterns(id *uuid.UUID) ([]models.FontSetPattern, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		fs.cache,
		fontSetPatternsCacheKey(id),
		utils.CacheTTLs[utils.RedisFontSetKey],
		func() ([]models.FontSetPattern, error) {
			return fs.repo.ListFontPatterns(id)
		},
	)
}

func (fs *FontService) invalidateFontSetPatternCache() error {
	if fs.cache == nil {
		return nil
	}
	if err := fs.cache.DeleteKeysByPattern(context.Background(), "*font_set_patterns*"); err != nil {
		return err
	}
	return fs.invalidateFontSetCache()
}

func (fs *FontService) GetFontSetPatternByID(id uuid.UUID) (*models.FontSetPattern, error) {
	return fs.repo.GetFontPatternByID(id)
}

func (fs *FontService) CreateFontSetPattern(obj *models.FontSetPattern) error {
	if err := fs.repo.CreateFontPattern(obj); err != nil {
		return err
	}
	return fs.invalidateFontSetPatternCache()
}

func (fs *FontService) UpdateFontSetPattern(obj *models.FontSetPattern) error {
	if err := fs.repo.UpdateFontPattern(obj); err != nil {
		return err
	}
	return fs.invalidateFontSetPatternCache()
}

func (fs *FontService) DeleteFontSetPattern(id uuid.UUID) error {
	if err := fs.repo.DeleteFontPattern(id); err != nil {
		return err
	}
	return fs.invalidateFontSetPatternCache()
}
