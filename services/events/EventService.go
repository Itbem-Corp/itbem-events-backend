package events

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/eventsrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListEvents() ([]models.Event, error) {
	cacheKey := "all:events"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.Event
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := eventsrepository.ListEvents(1, 0, "")
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["events"])

	return data, nil
}

func GetEventByID(id uuid.UUID) (*models.Event, error) {
	return eventsrepository.GetEventByID(id)
}

func CreateEvent(obj *models.Event) error {
	if err := eventsrepository.CreateEvent(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func UpdateEvent(obj *models.Event) error {
	if err := eventsrepository.UpdateEvent(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func DeleteEvent(id uuid.UUID) error {
	if err := eventsrepository.DeleteEvent(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}
