package events

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/eventtyperepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListEventTypes() ([]models.EventType, error) {
	cacheKey := "all:events"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.EventType
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := eventtyperepository.ListEventTypes()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["events"])

	return data, nil
}

func GetEventTypeByID(id uuid.UUID) (*models.EventType, error) {
	return eventtyperepository.GetEventTypeByID(id)
}

func CreateEventType(obj *models.EventType) error {
	if err := eventtyperepository.CreateEventType(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func UpdateEventType(obj *models.EventType) error {
	if err := eventtyperepository.UpdateEventType(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func DeleteEventType(id uuid.UUID) error {
	if err := eventtyperepository.DeleteEventType(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}
