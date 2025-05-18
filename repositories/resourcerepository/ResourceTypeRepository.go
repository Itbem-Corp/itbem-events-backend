package resourcerepository

import (
	"context"
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/redisrepository"
	"github.com/gofrs/uuid"
)

const redisResourceTypeKey = "resourceTypes"

func ListResourceTypes() ([]models.ResourceType, error) {
	var types []models.ResourceType
	err := gormrepository.GetList(&types, gormrepository.QueryOptions{
		OrderBy:  "id",
		OrderDir: "asc",
	})
	return types, err
}

func CreateResourceType(rt *models.ResourceType) error {
	err := gormrepository.Insert(rt)
	if err != nil {
		return ValidateError(err)
	}
	pattern := "*" + redisResourceTypeKey + "*"
	return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
}

func UpdateResourceType(rt *models.ResourceType) error {
	err := gormrepository.Update(rt, rt.ID)
	if err == nil {
		pattern := "*" + redisResourceTypeKey + "*"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return err
}

func DeleteResourceType(id uuid.UUID) error {
	err := gormrepository.Delete(id, &models.ResourceType{})
	if err == nil {
		pattern := "*" + redisResourceTypeKey + "*"
		return redisrepository.DeleteKeysByPattern(context.Background(), pattern)
	}
	return err
}

func GetResourceTypeByID(id uuid.UUID) (*models.ResourceType, error) {
	var rt models.ResourceType
	err := gormrepository.GetByID(&rt, id)
	return &rt, err
}
