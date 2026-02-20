package moments

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

// _momentSvc is the package-level singleton set by server.go.
var _momentSvc *MomentService

// SetDefaultMomentService wires the package-level functions to the DI instance.
func SetDefaultMomentService(svc *MomentService) { _momentSvc = svc }

func ListMoments() ([]models.Moment, error)              { return _momentSvc.ListMoments() }
func GetMomentByID(id uuid.UUID) (*models.Moment, error) { return _momentSvc.GetMomentByID(id) }
func CreateMoment(obj *models.Moment) error              { return _momentSvc.CreateMoment(obj) }
func UpdateMoment(obj *models.Moment) error              { return _momentSvc.UpdateMoment(obj) }
func DeleteMoment(id uuid.UUID) error                    { return _momentSvc.DeleteMoment(id) }

// MomentService is the injectable, struct-based moment service.
type MomentService struct {
	repo  ports.MomentRepository
	cache ports.CacheRepository
}

func NewMomentService(repo ports.MomentRepository, cache ports.CacheRepository) *MomentService {
	return &MomentService{repo: repo, cache: cache}
}

func (s *MomentService) ListMoments() ([]models.Moment, error) {
	cacheKey := "all:moments"
	ctx := context.Background()
	cached, err := s.cache.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Moment
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}
	data, err := s.repo.ListMoments()
	if err != nil {
		return nil, err
	}
	jsonStr, _ := json.Marshal(data)
	_ = s.cache.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["moments"])
	return data, nil
}

func (s *MomentService) GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	return s.repo.GetMomentByID(id)
}

func (s *MomentService) CreateMoment(obj *models.Moment) error {
	if err := s.repo.CreateMoment(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("moments", "all")
}

func (s *MomentService) UpdateMoment(obj *models.Moment) error {
	if err := s.repo.UpdateMoment(obj); err != nil {
		return err
	}
	return s.cache.Invalidate("moments", "all")
}

func (s *MomentService) DeleteMoment(id uuid.UUID) error {
	if err := s.repo.DeleteMoment(id); err != nil {
		return err
	}
	return s.cache.Invalidate("moments", "all")
}
