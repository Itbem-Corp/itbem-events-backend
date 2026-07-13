package templates

import (
	"context"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

var _templateSvc *DesignTemplateService

func SetDefaultDesignTemplateService(svc *DesignTemplateService) { _templateSvc = svc }

func templateServiceUnavailable() error {
	return fmt.Errorf("design template service not initialized")
}

func ListDesignTemplates() ([]models.DesignTemplate, error) {
	if _templateSvc == nil {
		return nil, templateServiceUnavailable()
	}
	return _templateSvc.ListDesignTemplates()
}

func GetDesignTemplateByID(id uuid.UUID) (*models.DesignTemplate, error) {
	if _templateSvc == nil {
		return nil, templateServiceUnavailable()
	}
	return _templateSvc.GetDesignTemplateByID(id)
}

func CreateDesignTemplate(obj *models.DesignTemplate) error {
	if _templateSvc == nil {
		return templateServiceUnavailable()
	}
	return _templateSvc.CreateDesignTemplate(obj)
}

func UpdateDesignTemplate(obj *models.DesignTemplate) error {
	if _templateSvc == nil {
		return templateServiceUnavailable()
	}
	return _templateSvc.UpdateDesignTemplate(obj)
}

func DeleteDesignTemplate(id uuid.UUID) error {
	if _templateSvc == nil {
		return templateServiceUnavailable()
	}
	return _templateSvc.DeleteDesignTemplate(id)
}

type DesignTemplateService struct {
	repo  ports.DesignTemplateRepository
	cache ports.CacheRepository
}

func NewDesignTemplateService(repo ports.DesignTemplateRepository, cache ports.CacheRepository) *DesignTemplateService {
	return &DesignTemplateService{repo: repo, cache: cache}
}

func (s *DesignTemplateService) ListDesignTemplates() ([]models.DesignTemplate, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisTemplatesKey,
		utils.CacheTTLs[utils.RedisTemplatesKey],
		s.repo.ListDesignTemplates,
	)
}

func (s *DesignTemplateService) GetDesignTemplateByID(id uuid.UUID) (*models.DesignTemplate, error) {
	return s.repo.GetDesignTemplateByID(id)
}

func (s *DesignTemplateService) CreateDesignTemplate(obj *models.DesignTemplate) error {
	if err := s.repo.CreateDesignTemplate(obj); err != nil {
		return err
	}
	return s.cache.Invalidate(utils.RedisTemplatesKey, "all")
}

func (s *DesignTemplateService) UpdateDesignTemplate(obj *models.DesignTemplate) error {
	if err := s.repo.UpdateDesignTemplate(obj); err != nil {
		return err
	}
	return s.cache.Invalidate(utils.RedisTemplatesKey, "all")
}

func (s *DesignTemplateService) DeleteDesignTemplate(id uuid.UUID) error {
	if err := s.repo.DeleteDesignTemplate(id); err != nil {
		return err
	}
	return s.cache.Invalidate(utils.RedisTemplatesKey, "all")
}
