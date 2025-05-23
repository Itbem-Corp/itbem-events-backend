package momenttyperepository

import (
    "events-stocks/models"
    "events-stocks/repositories/gormrepository"
    "github.com/gofrs/uuid"
)

func CreateMomentType(m *models.MomentType) error {
    return gormrepository.Insert(m)
}

func UpdateMomentType(m *models.MomentType) error {
    return gormrepository.Update(m, m.ID)
}

func DeleteMomentType(id uuid.UUID) error {
    return gormrepository.Delete(id, &models.MomentType{})
}

func GetMomentTypeByID(id uuid.UUID) (*models.MomentType, error) {
    var model models.MomentType
    err := gormrepository.GetByID(&model, id)
    return &model, err
}

func ListMomentTypes() ([]models.MomentType, error) {
    var list []models.MomentType
    err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
    return list, err
}
