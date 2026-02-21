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

// IncrementAnalytics atomically increments one analytics counter for an event.
// Call as a goroutine -- never blocks the main request.
func IncrementAnalytics(eventID uuid.UUID, field string) {
	analytics, err := GetEventAnalyticsByEventID(eventID)
	if err != nil || analytics == nil {
		analytics = &models.EventAnalytics{EventID: eventID}
		applyIncrement(analytics, field)
		_ = CreateEventAnalytics(analytics)
		return
	}
	applyIncrement(analytics, field)
	_ = UpdateEventAnalytics(analytics)
}

func applyIncrement(a *models.EventAnalytics, field string) {
	switch field {
	case "views":
		a.Views++
	case "rsvp_confirmed":
		a.RSVPConfirmed++
	case "rsvp_declined":
		a.RSVPDeclined++
	case "moment_uploads":
		a.MomentUploads++
	}
}
