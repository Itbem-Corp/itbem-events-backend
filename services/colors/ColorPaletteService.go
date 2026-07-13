package colors

import (
	"context"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListColorPalettes() ([]models.ColorPalette, error) {
	if _colorSvc == nil {
		return nil, colorServiceUnavailable()
	}
	return _colorSvc.ListColorPalettes()
}

func GetColorPaletteByID(id uuid.UUID) (*models.ColorPalette, error) {
	if _colorSvc == nil {
		return nil, colorServiceUnavailable()
	}
	return _colorSvc.GetColorPaletteByID(id)
}

func CreateColorPalette(obj *models.ColorPalette) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.CreateColorPalette(obj)
}

func UpdateColorPalette(obj *models.ColorPalette) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.UpdateColorPalette(obj)
}

func DeleteColorPalette(id uuid.UUID) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.DeleteColorPalette(id)
}

func (s *ColorService) ListColorPalettes() ([]models.ColorPalette, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisColorPalettesKey,
		utils.CacheTTLs[utils.RedisColorPalettesKey],
		s.repo.ListColorPalettes,
	)
}

func (s *ColorService) GetColorPaletteByID(id uuid.UUID) (*models.ColorPalette, error) {
	return s.repo.GetColorPaletteByID(id)
}

func (s *ColorService) CreateColorPalette(obj *models.ColorPalette) error {
	if err := s.repo.CreatePalette(obj); err != nil {
		return err
	}
	return s.invalidatePaletteCatalogs()
}

func (s *ColorService) UpdateColorPalette(obj *models.ColorPalette) error {
	if err := s.repo.UpdatePalette(obj); err != nil {
		return err
	}
	return s.invalidatePaletteCatalogs()
}

func (s *ColorService) DeleteColorPalette(id uuid.UUID) error {
	if err := s.repo.DeletePalette(id); err != nil {
		return err
	}
	return s.invalidatePaletteCatalogs()
}
