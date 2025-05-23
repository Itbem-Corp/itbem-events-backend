package resourcerepository

import (
	"events-stocks/models"
	"events-stocks/repositories/gormrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListResourceTypesRaw() ([]models.ResourceType, error) {
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
	return redisrepository.Invalidate(utils.RedisResourceTypeKey, "all")
}

func UpdateResourceType(rt *models.ResourceType) error {
	err := gormrepository.Update(rt, rt.ID)
	if err == nil {
		return redisrepository.Invalidate(utils.RedisResourceTypeKey, "all")
	}
	return err
}

func DeleteResourceType(id uuid.UUID) error {
	err := gormrepository.Delete(id, &models.ResourceType{})
	if err == nil {
		return redisrepository.Invalidate(utils.RedisResourceTypeKey, "all")
	}
	return err
}
