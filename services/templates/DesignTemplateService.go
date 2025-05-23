package templates

import (
	"events-stocks/models"
	"events-stocks/repositories/redisrepository"
	"events-stocks/repositories/templatesrepository"
	"github.com/gofrs/uuid"
)

func GetDesignTemplateByID(id uuid.UUID) (*models.DesignTemplate, error) {
	return templatesrepository.GetDesignTemplateByID(id)
}

func CreateDesignTemplate(obj *models.DesignTemplate) error {
	if err := templatesrepository.CreateDesignTemplate(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("templates", "all")
}

func UpdateDesignTemplate(obj *models.DesignTemplate) error {
	if err := templatesrepository.UpdateDesignTemplate(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("templates", "all")
}

func DeleteDesignTemplate(id uuid.UUID) error {
	if err := templatesrepository.DeleteDesignTemplate(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("templates", "all")
}
