package events

import (
	"context"
	"encoding/json"
	"events-stocks/models"
	"events-stocks/repositories/eventanalyticsrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
)

func ListEventAnalyticss() ([]models.EventAnalytics, error) {
	cacheKey := "all:events"
	ctx := context.Background()

	cached, err := redisrepository.GetKey(ctx, cacheKey)
	if err == nil && cached != "" {
		var result []models.EventAnalytics
		if err := json.Unmarshal([]byte(cached), &result); err == nil {
			return result, nil
		}
	}

	data, err := eventanalyticsrepository.ListEventAnalyticss()
	if err != nil {
		return nil, err
	}

	jsonStr, _ := json.Marshal(data)
	_ = redisrepository.SaveKey(ctx, cacheKey, string(jsonStr), utils.CacheTTLs["events"])

	return data, nil
}

func GetEventAnalyticsByID(id uuid.UUID) (*models.EventAnalytics, error) {
	return eventanalyticsrepository.GetEventAnalyticsByID(id)
}

func CreateEventAnalytics(obj *models.EventAnalytics) error {
	if err := eventanalyticsrepository.CreateEventAnalytics(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func UpdateEventAnalytics(obj *models.EventAnalytics) error {
	if err := eventanalyticsrepository.UpdateEventAnalytics(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}

func DeleteEventAnalytics(id uuid.UUID) error {
	if err := eventanalyticsrepository.DeleteEventAnalytics(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("events", "all")
}
