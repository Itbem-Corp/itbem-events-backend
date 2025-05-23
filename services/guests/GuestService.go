package guests

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/guestrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListGuests() ([]models.Guest, error) {
	cacheKey := "all:guests"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Guest
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := guestrepository.ListGuests()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["guests"])

	return data, nil
}

func GetGuestByID(id uuid.UUID) (*models.Guest, error) {
	return guestrepository.GetGuestByID(id)
}

func CreateGuest(obj *models.Guest) error {
	if err := guestrepository.CreateGuest(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("guests", "all")
}

func UpdateGuest(obj *models.Guest) error {
	if err := guestrepository.UpdateGuest(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("guests", "all")
}

func DeleteGuest(id uuid.UUID) error {
	if err := guestrepository.DeleteGuest(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("guests", "all")
}
