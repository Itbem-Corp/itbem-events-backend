package moments

import (
	"context"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

var _momentTypeSvc *MomentTypeService

func SetDefaultMomentTypeService(svc *MomentTypeService) { _momentTypeSvc = svc }

func momentTypeServiceUnavailable() error {
	return fmt.Errorf("moment type service not initialized")
}

func ListMomentTypes() ([]models.MomentType, error) {
	if _momentTypeSvc == nil {
		return nil, momentTypeServiceUnavailable()
	}
	return _momentTypeSvc.ListMomentTypes()
}

func GetMomentTypeByID(id uuid.UUID) (*models.MomentType, error) {
	if _momentTypeSvc == nil {
		return nil, momentTypeServiceUnavailable()
	}
	return _momentTypeSvc.GetMomentTypeByID(id)
}

func CreateMomentType(obj *models.MomentType) error {
	if _momentTypeSvc == nil {
		return momentTypeServiceUnavailable()
	}
	return _momentTypeSvc.CreateMomentType(obj)
}

func UpdateMomentType(obj *models.MomentType) error {
	if _momentTypeSvc == nil {
		return momentTypeServiceUnavailable()
	}
	return _momentTypeSvc.UpdateMomentType(obj)
}

func DeleteMomentType(id uuid.UUID) error {
	if _momentTypeSvc == nil {
		return momentTypeServiceUnavailable()
	}
	return _momentTypeSvc.DeleteMomentType(id)
}

type MomentTypeService struct {
	repo  ports.MomentTypeRepository
	cache ports.CacheRepository
}

func NewMomentTypeService(repo ports.MomentTypeRepository, cache ports.CacheRepository) *MomentTypeService {
	return &MomentTypeService{repo: repo, cache: cache}
}

func (s *MomentTypeService) ListMomentTypes() ([]models.MomentType, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:moment_types",
		utils.CacheTTLs[utils.RedisMomentsKey],
		s.repo.ListMomentTypes,
	)
}

func (s *MomentTypeService) GetMomentTypeByID(id uuid.UUID) (*models.MomentType, error) {
	return s.repo.GetMomentTypeByID(id)
}

func (s *MomentTypeService) CreateMomentType(obj *models.MomentType) error {
	if err := s.repo.CreateMomentType(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("moment_types", "all")
}

func (s *MomentTypeService) UpdateMomentType(obj *models.MomentType) error {
	if err := s.repo.UpdateMomentType(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("moment_types", "all")
}

func (s *MomentTypeService) DeleteMomentType(id uuid.UUID) error {
	if err := s.repo.DeleteMomentType(id); err != nil {
		return err
	}
	return s.cache.Invalidate("moment_types", "all")
}
