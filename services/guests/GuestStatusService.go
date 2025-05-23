package guests

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/gueststatusrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListGuestStatuss() ([]models.GuestStatus, error) {
	cacheKey := "all:guests"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.GuestStatus
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := gueststatusrepository.ListGuestStatuss()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["guests"])

	return data, nil
}

func GetGuestStatusByID(id uuid.UUID) (*models.GuestStatus, error) {
	return gueststatusrepository.GetGuestStatusByID(id)
}

func CreateGuestStatus(obj *models.GuestStatus) error {
	if err := gueststatusrepository.CreateGuestStatus(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("guests", "all")
}

func UpdateGuestStatus(obj *models.GuestStatus) error {
	if err := gueststatusrepository.UpdateGuestStatus(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("guests", "all")
}

func DeleteGuestStatus(id uuid.UUID) error {
	if err := gueststatusrepository.DeleteGuestStatus(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("guests", "all")
}
