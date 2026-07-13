package colors

import (
	"context"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListColorPalettePatterns() ([]models.ColorPalettePattern, error) {
	if _colorSvc == nil {
		return nil, colorServiceUnavailable()
	}
	return _colorSvc.ListColorPalettePatterns()
}

func GetColorPalettePatternByID(id uuid.UUID) (*models.ColorPalettePattern, error) {
	if _colorSvc == nil {
		return nil, colorServiceUnavailable()
	}
	return _colorSvc.GetColorPalettePatternByID(id)
}

func CreateColorPalettePattern(obj *models.ColorPalettePattern) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.CreateColorPalettePattern(obj)
}

func UpdateColorPalettePattern(obj *models.ColorPalettePattern) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.UpdateColorPalettePattern(obj)
}

func DeleteColorPalettePattern(id uuid.UUID) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.DeleteColorPalettePattern(id)
}

func (s *ColorService) ListColorPalettePatterns() ([]models.ColorPalettePattern, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisColorPalettePatternsKey,
		utils.CacheTTLs[utils.RedisColorPalettePatternsKey],
		s.repo.ListAllPatterns,
	)
}

func (s *ColorService) GetColorPalettePatternByID(id uuid.UUID) (*models.ColorPalettePattern, error) {
	return s.repo.GetColorPatternByID(id)
}

func (s *ColorService) CreateColorPalettePattern(obj *models.ColorPalettePattern) error {
	if err := s.repo.CreatePattern(obj); err != nil {
		return err
	}
	return s.invalidatePalettePatternCatalogs()
}

func (s *ColorService) UpdateColorPalettePattern(obj *models.ColorPalettePattern) error {
	if err := s.repo.UpdatePattern(obj); err != nil {
		return err
	}
	return s.invalidatePalettePatternCatalogs()
}

func (s *ColorService) DeleteColorPalettePattern(id uuid.UUID) error {
	if err := s.repo.DeletePattern(id); err != nil {
		return err
	}
	return s.invalidatePalettePatternCatalogs()
}
