package moments

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/momenttyperepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListMomentTypes() ([]models.MomentType, error) {
	cacheKey := "all:moments"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.MomentType
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := momenttyperepository.ListMomentTypes()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["moments"])

	return data, nil
}

func GetMomentTypeByID(id uuid.UUID) (*models.MomentType, error) {
	return momenttyperepository.GetMomentTypeByID(id)
}

func CreateMomentType(obj *models.MomentType) error {
	if err := momenttyperepository.CreateMomentType(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("moments", "all")
}

func UpdateMomentType(obj *models.MomentType) error {
	if err := momenttyperepository.UpdateMomentType(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("moments", "all")
}

func DeleteMomentType(id uuid.UUID) error {
	if err := momenttyperepository.DeleteMomentType(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("moments", "all")
}
