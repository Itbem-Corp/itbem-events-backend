package events

import (
	"encoding/json"
	"errors"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/authz"
	"events-stocks/models"
	jobqueuerepository "events-stocks/repositories/jobqueuerepository"
	"log/slog"
	"math"
	"net/http"
	"sort"
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
	var performance []dtos.PerformanceMetricSummary
	var analyticsErr, rollupErr, performanceErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		analytics, analyticsErr = eventsService.GetEventAnalyticsBaseByEventID(eventID)
	}()
	go func() {
		defer wait.Done()
		rollup, rollupErr = eventsService.GetEventAnalyticsRollupByEventID(eventID)
	}()
	go func() {
		defer wait.Done()
		performance, performanceErr = loadPerformanceSummary(eventID, 7, time.Now().UTC())
	}()
	wait.Wait()
	if performanceErr != nil {
		slog.Warn("performance analytics unavailable", "event_id", eventID, "error", performanceErr)
		performance = []dtos.PerformanceMetricSummary{}
	}
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
			response.Performance = performance
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
	response.Performance = performance
	requestAnalyticsRollup(eventID)
	return utils.Success(c, http.StatusOK, "Analytics loaded", response)
}

type performanceSummaryKey struct {
	route  string
	metric string
}

type performanceAccumulator struct {
	count          int64
	histogramCount int64
	sum            float64
	min            float64
	max            float64
	buckets        map[int]models.EventPerformanceBucketDaily
}

func loadPerformanceSummary(eventID uuid.UUID, days int, now time.Time) ([]dtos.PerformanceMetricSummary, error) {
	if configuration.DB == nil {
		return []dtos.PerformanceMetricSummary{}, nil
	}
	if days < 1 {
		days = 7
	}
	from := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	var aggregates []models.EventPerformanceDaily
	if err := configuration.DB.Where("event_id = ? AND bucket_date >= ?", eventID, from).Find(&aggregates).Error; err != nil {
		return nil, err
	}
	var histogram []models.EventPerformanceBucketDaily
	if err := configuration.DB.Where("event_id = ? AND bucket_date >= ?", eventID, from).Find(&histogram).Error; err != nil {
		return nil, err
	}
	accumulators := map[performanceSummaryKey]*performanceAccumulator{}
	for _, row := range aggregates {
		key := performanceSummaryKey{route: row.Route, metric: row.Metric}
		acc := accumulators[key]
		if acc == nil {
			acc = &performanceAccumulator{min: row.ValueMin, max: row.ValueMax, buckets: map[int]models.EventPerformanceBucketDaily{}}
			accumulators[key] = acc
		}
		acc.count += row.SampleCount
		acc.sum += row.ValueSum
		if row.ValueMin < acc.min {
			acc.min = row.ValueMin
		}
		if row.ValueMax > acc.max {
			acc.max = row.ValueMax
		}
	}
	for _, row := range histogram {
		key := performanceSummaryKey{route: row.Route, metric: row.Metric}
		acc := accumulators[key]
		if acc == nil {
			continue
		}
		bucket := acc.buckets[row.BucketIndex]
		bucket.BucketIndex, bucket.UpperBound = row.BucketIndex, row.UpperBound
		bucket.SampleCount += row.SampleCount
		acc.histogramCount += row.SampleCount
		acc.buckets[row.BucketIndex] = bucket
	}
	result := make([]dtos.PerformanceMetricSummary, 0, len(accumulators))
	for key, acc := range accumulators {
		if acc.count == 0 || len(acc.buckets) == 0 {
			continue
		}
		p75 := histogramPercentile(acc.buckets, acc.histogramCount, 0.75)
		p95 := histogramPercentile(acc.buckets, acc.histogramCount, 0.95)
		result = append(result, dtos.PerformanceMetricSummary{
			Route: key.route, Metric: key.metric, SampleCount: acc.count,
			Average: acc.sum / float64(acc.count), Minimum: acc.min, Maximum: acc.max,
			P75: p75, P95: p95, Rating: performanceRating(key.metric, p75),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Route == result[j].Route {
			return result[i].Metric < result[j].Metric
		}
		return result[i].Route < result[j].Route
	})
	return result, nil
}

func histogramPercentile(buckets map[int]models.EventPerformanceBucketDaily, total int64, percentile float64) float64 {
	if total <= 0 || len(buckets) == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(total) * percentile))
	indexes := make([]int, 0, len(buckets))
	for index := range buckets {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	var cumulative int64
	for _, index := range indexes {
		bucket := buckets[index]
		cumulative += bucket.SampleCount
		if cumulative >= target {
			return bucket.UpperBound
		}
	}
	return buckets[indexes[len(indexes)-1]].UpperBound
}

func performanceRating(metric string, p75 float64) string {
	switch metric {
	case "lcp":
		if p75 <= 2500 {
			return "good"
		}
		if p75 <= 4000 {
			return "needs_improvement"
		}
	case "inp":
		if p75 <= 200 {
			return "good"
		}
		if p75 <= 500 {
			return "needs_improvement"
		}
	case "cls":
		if p75 <= 0.1 {
			return "good"
		}
		if p75 <= 0.25 {
			return "needs_improvement"
		}
	default:
		return ""
	}
	return "poor"
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
