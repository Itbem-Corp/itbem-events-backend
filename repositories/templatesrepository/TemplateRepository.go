package templatesrepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func CreateDesignTemplate(m *models.DesignTemplate) error {
	return gormrepository.Insert(m)
}

func UpdateDesignTemplate(m *models.DesignTemplate) error {
	return gormrepository.Update(m, m.ID)
}

func DeleteDesignTemplate(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.DesignTemplate{})
}

func GetDesignTemplateByID(id uuid.UUID) (*models.DesignTemplate, error) {
	var model models.DesignTemplate
	err := gormrepository.GetByID(&model, id)
	return &model, err
}

func ListDesignTemplates() ([]models.DesignTemplate, error) {
	var list []models.DesignTemplate
	err := gormrepository.GetList(&list, gormrepository.QueryOptions{})
	return list, err
}
