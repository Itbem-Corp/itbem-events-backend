package guests

import (
	"context"
	"fmt"

	"events-stocks/models"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

var _guestStatusSvc *GuestStatusService

func SetDefaultGuestStatusService(svc *GuestStatusService) { _guestStatusSvc = svc }

func guestStatusServiceUnavailable() error {
	return fmt.Errorf("guest status service not initialized")
}

func ListGuestStatuss() ([]models.GuestStatus, error) {
	if _guestStatusSvc == nil {
		return nil, guestStatusServiceUnavailable()
	}
	return _guestStatusSvc.ListGuestStatuss()
}

func GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	if _guestStatusSvc == nil {
		return nil, guestStatusServiceUnavailable()
	}
	return _guestStatusSvc.GetGuestStatusByID(id)
}

func CreateGuestStatus(obj *models.GuestStatus) error {
	if _guestStatusSvc == nil {
		return guestStatusServiceUnavailable()
	}
	return _guestStatusSvc.CreateGuestStatus(obj)
}

func UpdateGuestStatus(obj *models.GuestStatus) error {
	if _guestStatusSvc == nil {
		return guestStatusServiceUnavailable()
	}
	return _guestStatusSvc.UpdateGuestStatus(obj)
}

func DeleteGuestStatus(id uuid.UUID) error {
	if _guestStatusSvc == nil {
		return guestStatusServiceUnavailable()
	}
	return _guestStatusSvc.DeleteGuestStatus(id)
}

type GuestStatusService struct {
	repo  ports.GuestStatusRepository
	cache ports.CacheRepository
}

func NewGuestStatusService(repo ports.GuestStatusRepository, cache ports.CacheRepository) *GuestStatusService {
	return &GuestStatusService{repo: repo, cache: cache}
}

func (s *GuestStatusService) ListGuestStatuss() ([]models.GuestStatus, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		s.cache,
		"all:"+utils.RedisGuestStatussKey,
		utils.CacheTTLs[utils.RedisGuestStatussKey],
		s.repo.ListGuestStatuss,
	)
}

func (s *GuestStatusService) GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	return s.repo.GetGuestStatusByID(id)
}

func (s *GuestStatusService) CreateGuestStatus(obj *models.GuestStatus) error {
	if err := s.repo.CreateGuestStatus(obj); err != nil {
		return err
	}
	return s.invalidateGuestStatusCache()
}

func (s *GuestStatusService) UpdateGuestStatus(obj *models.GuestStatus) error {
	if err := s.repo.UpdateGuestStatus(obj); err != nil {
		return err
	}
	return s.invalidateGuestStatusCache()
}

func (s *GuestStatusService) DeleteGuestStatus(id uuid.UUID) error {
	if err := s.repo.DeleteGuestStatus(id); err != nil {
		return err
	}
	return s.invalidateGuestStatusCache()
}

func (s *GuestStatusService) invalidateGuestStatusCache() error {
	if s.cache == nil {
		return nil
	}
	return s.cache.Invalidate(utils.RedisGuestStatussKey, "all")
}
