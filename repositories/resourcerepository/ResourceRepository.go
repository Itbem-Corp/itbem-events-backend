package resourcerepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"github.com/gofrs/uuid"
)

func CreateResource(resource *models.Resource) error {
	return gormrepository.Insert(resource)
}

func UpdateResource(resource *models.Resource) error {
	return gormrepository.Update(resource, resource.ID)
}

func DeleteResource(id uuid.UUID) error {
	return gormrepository.Delete(id, &models.Resource{})
}

func GetResourceByID(id uuid.UUID) (*models.Resource, error) {
	var resource models.Resource
	err := gormrepository.GetByID(&resource, id, "ResourceType", "EventSection")
	return &resource, err
}

func ListResourcesBySection(sectionID uuid.UUID) ([]models.Resource, error) {
	var resources []models.Resource

	filters := map[string]interface{}{
		"event_section_id": sectionID,
	}

	err := gormrepository.GetList(&resources, gormrepository.QueryOptions{
		Filters:  filters,
		OrderBy:  "position",
		OrderDir: "asc",
	})
	return resources, err
}
