package events

import (
	"encoding/json"
	"errors"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	jobqueuerepository "events-stocks/repositories/jobqueuerepository"
	"log/slog"
	"net/http"
	"sync"
	"time"

	eventsService "events-stocks/services/events"
	"events-stocks/utils"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

const analyticsRollupMaxAge = time.Minute

// GetEventAnalytics returns the analytics record for a given event.
func GetEventAnalytics(c echo.Context) error {
	idStr := c.Param("id")
	eventID, err := uuid.FromString(idStr)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid event ID", err.Error())
	}
	if _, _, authErr := authz.RequireEventCapability(c, eventID, authz.CapabilityAnalyticsView); authErr != nil {
		return authz.Respond(c, authErr)
	}

	var analytics *models.EventAnalytics
	var rollup *models.EventAnalyticsRollup
	var analyticsErr, rollupErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		analytics, analyticsErr = eventsService.GetEventAnalyticsBaseByEventID(eventID)
	}()
	go func() {
		defer wait.Done()
		rollup, rollupErr = eventsService.GetEventAnalyticsRollupByEventID(eventID)
	}()
	wait.Wait()
	if analyticsErr != nil {
		if errors.Is(analyticsErr, gorm.ErrRecordNotFound) {
			analytics = &models.EventAnalytics{EventID: eventID}
			if createErr := eventsService.CreateEventAnalytics(analytics); createErr != nil {
				return utils.Error(c, http.StatusInternalServerError, "Error creating analytics", createErr.Error())
			}
		} else {
			return utils.Error(c, http.StatusInternalServerError, "Error fetching analytics", analyticsErr.Error())
		}
	}

	if rollupErr == nil && rollup != nil {
		var guestSummary dtos.GuestAnalyticsSummary
		if err := json.Unmarshal([]byte(rollup.GuestSummary), &guestSummary); err == nil {
			applyAnalyticsRollup(analytics, rollup)
			response := dtos.NewEventAnalyticsResponse(analytics)
			response.Guests = guestSummary
			if rollup.ComputedAt.IsZero() || time.Since(rollup.ComputedAt) >= analyticsRollupMaxAge {
				requestAnalyticsRollup(eventID)
			}
			return utils.Success(c, http.StatusOK, "Analytics loaded", response)
		} else {
			slog.Warn("invalid analytics rollup payload; using live fallback", "event_id", eventID, "error", err)
		}
	} else if rollupErr != nil && !errors.Is(rollupErr, gorm.ErrRecordNotFound) {
		slog.Warn("analytics rollup unavailable; using live fallback", "event_id", eventID, "error", rollupErr)
	}

	// Compatibility path for the first read or when workers are disabled. Once a
	// snapshot exists, subsequent reads avoid this O(number of guests) request work.
	response, err := loadLiveAnalyticsFallback(eventID)
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Error fetching analytics", err.Error())
	}
	requestAnalyticsRollup(eventID)
	return utils.Success(c, http.StatusOK, "Analytics loaded", response)
}

func loadLiveAnalyticsFallback(eventID uuid.UUID) (dtos.EventAnalyticsResponse, error) {
	var guestProjection []dtos.AnalyticsGuest
	var analytics *models.EventAnalytics
	var guestErr, analyticsErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		guestProjection, guestErr = eventGuestSvc.ListAnalyticsGuestsByEventID(eventID)
	}()
	go func() {
		defer wait.Done()
		analytics, analyticsErr = eventsService.GetEventAnalyticsByEventID(eventID)
	}()
	wait.Wait()
	if guestErr != nil {
		return dtos.EventAnalyticsResponse{}, guestErr
	}
	if analyticsErr != nil {
		return dtos.EventAnalyticsResponse{}, analyticsErr
	}
	response := dtos.NewEventAnalyticsResponse(analytics)
	response.Guests = dtos.NewGuestAnalyticsSummary(guestProjection)
	return response, nil
}

func applyAnalyticsRollup(analytics *models.EventAnalytics, rollup *models.EventAnalyticsRollup) {
	analytics.MomentTotal = rollup.MomentTotal
	analytics.MomentApproved = rollup.MomentApproved
	analytics.MomentPending = rollup.MomentPending
	if rollup.MomentComments > analytics.MomentComments {
		analytics.MomentComments = rollup.MomentComments
	}
	if rollup.MomentUploads > analytics.MomentUploads {
		analytics.MomentUploads = rollup.MomentUploads
	}
}

func requestAnalyticsRollup(eventID uuid.UUID) {
	go func() {
		if _, err := jobqueuerepository.PublishAnalyticsRollup(eventID); err != nil {
			slog.Warn("failed to publish analytics rollup", "event_id", eventID, "error", err)
		}
	}()
}
