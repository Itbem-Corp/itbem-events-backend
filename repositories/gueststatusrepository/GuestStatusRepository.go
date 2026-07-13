package gueststatusrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func CreateGuestStatus(m *models.GuestStatus) error {
	return gormrepository.Insert(m)
}

func UpdateGuestStatus(m *models.GuestStatus) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteGuestStatus(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.GuestStatus{})
}

func GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	var model models.GuestStatus
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListGuestStatuss() ([]models.GuestStatus, error) {
	var list []models.GuestStatus
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}

func GetGuestStatusByCode(code string) (*models.GuestStatus, error) {
	var status models.GuestStatus
	err := gormrepository.GetFirst(&status, gormrepository.QueryOptions{
		Filters: map[string]interface{}{
			"code": code,
		},
	})
	if err != nil {
		return nil, err
	}
	return &status, nil
}

// GuestStatusRepo implements ports.GuestStatusRepository.
type GuestStatusRepo struct{}

func NewGuestStatusRepo() *GuestStatusRepo { return &GuestStatusRepo{} }

func (r *GuestStatusRepo) CreateGuestStatus(m *models.GuestStatus) error {
	return CreateGuestStatus(m)
}
func (r *GuestStatusRepo) UpdateGuestStatus(m *models.GuestStatus) error {
	return UpdateGuestStatus(m)
}
func (r *GuestStatusRepo) DeleteGuestStatus(id uuid.UUID) error {
	return DeleteGuestStatus(id)
}
func (r *GuestStatusRepo) GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	return GetGuestStatusByID(id)
}
func (r *GuestStatusRepo) ListGuestStatuss() ([]models.GuestStatus, error) {
	return ListGuestStatuss()
}
