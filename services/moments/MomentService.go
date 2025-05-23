package moments

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/momentrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListMoments() ([]models.Moment, error) {
	cacheKey := "all:moments"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Moment
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := momentrepository.ListMoments()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["moments"])

	return data, nil
}

func GetMomentByID(id uuid.UUID) (*models.Moment, error) {
	return momentrepository.GetMomentByID(id)
}

func CreateMoment(obj *models.Moment) error {
	if err := momentrepository.CreateMoment(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("moments", "all")
}

func UpdateMoment(obj *models.Moment) error {
	if err := momentrepository.UpdateMoment(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("moments", "all")
}

func DeleteMoment(id uuid.UUID) error {
	if err := momentrepository.DeleteMoment(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("moments", "all")
}
