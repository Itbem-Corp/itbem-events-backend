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
	cacheKey := "all:event_analytics"
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
	return redisrepository.Invalidate("event_analytics", "all")
}

func UpdateEventAnalytics(obj *models.EventAnalytics) error {
	if err := eventanalyticsrepository.UpdateEventAnalytics(obj); err != nil {
		return err
	}
	return redisrepository.Invalidate("event_analytics", "all")
}

func DeleteEventAnalytics(id uuid.UUID) error {
	if err := eventanalyticsrepository.DeleteEventAnalytics(id); err != nil {
		return err
	}
	return redisrepository.Invalidate("event_analytics", "all")
}

// GetEventAnalyticsByEventID fetches the analytics record for a given event.
func GetEventAnalyticsByEventID(eventID uuid.UUID) (*models.EventAnalytics, error) {
	return eventanalyticsrepository.GetEventAnalyticsByEventID(eventID)
}
