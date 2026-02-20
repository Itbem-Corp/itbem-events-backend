package momentrepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateMoment(m *models.Moment) error {
    return gormrepository.Insert(m)
}

func UpdateMoment(m *models.Moment) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteMoment(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.Moment{})
}

func GetMomentByID(id uuid.UUID) (*models.Moment, error) {
    var model models.Moment
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListMoments() ([]models.Moment, error) {
    var list []models.Moment
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}

// MomentRepo implements ports.MomentRepository.
type MomentRepo struct{}

func NewMomentRepo() *MomentRepo { return &MomentRepo{} }

func (r *MomentRepo) CreateMoment(m *models.Moment) error                { return CreateMoment(m) }
func (r *MomentRepo) UpdateMoment(m *models.Moment) error                { return UpdateMoment(m) }
func (r *MomentRepo) DeleteMoment(id uuid.UUID) error                    { return DeleteMoment(id) }
func (r *MomentRepo) GetMomentByID(id uuid.UUID) (*models.Moment, error) { return GetMomentByID(id) }
func (r *MomentRepo) ListMoments() ([]models.Moment, error)              { return ListMoments() }
