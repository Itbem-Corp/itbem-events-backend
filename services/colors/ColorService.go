package colors

import (
	"context"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

var _colorSvc *ColorService

func SetDefaultColorService(svc *ColorService) { _colorSvc = svc }

func colorServiceUnavailable() error {
	return fmt.Errorf("color service not initialized")
}

func ListColorCollection() ([]models.Color, error) {
	if _colorSvc == nil {
		return nil, colorServiceUnavailable()
	}
	return _colorSvc.ListColorCollection()
}

func GetColorByID(id uuid.UUID) (*models.Color, error) {
	if _colorSvc == nil {
		return nil, colorServiceUnavailable()
	}
	return _colorSvc.GetColorByID(id)
}

func CreateColor(obj *models.Color) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.CreateColor(obj)
}

func UpdateColor(obj *models.Color) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.UpdateColor(obj)
}

func DeleteColor(id uuid.UUID) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.DeleteColor(id)
}

func CreateMultipleColors(colors []models.Color) error {
	if _colorSvc == nil {
		return colorServiceUnavailable()
	}
	return _colorSvc.CreateMultipleColors(colors)
}

type ColorService struct {
	repo  ports.ColorRepository
	cache ports.CacheRepository
}

func NewColorService(repo ports.ColorRepository, cache ports.CacheRepository) *ColorService {
	return &ColorService{repo: repo, cache: cache}
}

func (s *ColorService) ListColorCollection() ([]models.Color, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisColorsServiceKey,
		utils.CacheTTLs[utils.RedisColorsServiceKey],
		s.repo.ListColors,
	)
}

func (s *ColorService) GetColorByID(id uuid.UUID) (*models.Color, error) {
	return s.repo.GetColorByID(id)
}

func (s *ColorService) CreateColor(obj *models.Color) error {
	if err := s.repo.CreateColor(obj); err != nil {
		return err
	}
	return s.invalidateColorDependentCatalogs()
}

func (s *ColorService) UpdateColor(obj *models.Color) error {
	if err := s.repo.UpdateColor(obj); err != nil {
		return err
	}
	return s.invalidateColorDependentCatalogs()
}

func (s *ColorService) DeleteColor(id uuid.UUID) error {
	if err := s.repo.DeleteColor(id); err != nil {
		return err
	}
	return s.invalidateColorDependentCatalogs()
}

func (s *ColorService) CreateMultipleColors(colors []models.Color) error {
	if len(colors) == 0 {
		return fmt.Errorf("no colors provided")
	}
	if err := s.repo.CreateMultipleColors(colors); err != nil {
		return err
	}
	return s.invalidateColorDependentCatalogs()
}

func (s *ColorService) invalidateCaches(resources ...string) error {
	if s.cache == nil {
		return nil
	}
	for _, resource := range resources {
		_ = s.cache.Invalidate(resource, "all")
	}
	return nil
}

func (s *ColorService) invalidateColorDependentCatalogs() error {
	return s.invalidateCaches(
		utils.RedisColorsServiceKey,
		utils.RedisColorPalettesKey,
		utils.RedisColorPalettePatternsKey,
	)
}

func (s *ColorService) invalidatePaletteCatalogs() error {
	return s.invalidateCaches(utils.RedisColorPalettesKey)
}

func (s *ColorService) invalidatePalettePatternCatalogs() error {
	return s.invalidateCaches(utils.RedisColorPalettePatternsKey, utils.RedisColorPalettesKey)
}
