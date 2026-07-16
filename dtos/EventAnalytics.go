package dtos

import (
	"events-stocks/models"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

type AnalyticsCount struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type AnalyticsTimelinePoint struct {
	Date      string `json:"date"`
	Confirmed int    `json:"confirmed"`
	Declined  int    `json:"declined"`
}

type AnalyticsTopCompanion struct {
	ID             uuid.UUID `json:"id"`
	FirstName      string    `json:"first_name"`
	LastName       string    `json:"last_name"`
	Role           string    `json:"role"`
	CompanionCount int       `json:"companion_count"`
}

type GuestAnalyticsSummary struct {
	TotalGuests        int                      `json:"total_guests"`
	Confirmed          int                      `json:"confirmed"`
	Declined           int                      `json:"declined"`
	Pending            int                      `json:"pending"`
	TotalCompanions    int                      `json:"total_companions"`
	EstimatedAttendees int                      `json:"estimated_attendees"`
	Dietary            []AnalyticsCount         `json:"dietary"`
	Methods            []AnalyticsCount         `json:"methods"`
	Roles              []AnalyticsCount         `json:"roles"`
	Tables             []AnalyticsCount         `json:"tables"`
	Timeline           []AnalyticsTimelinePoint `json:"timeline"`
	TopCompanions      []AnalyticsTopCompanion  `json:"top_companions"`
}

type EventAnalyticsResponse struct {
	ID             uuid.UUID                  `json:"id"`
	EventID        uuid.UUID                  `json:"event_id"`
	Views          int                        `json:"views"`
	MomentComments int                        `json:"moment_comments"`
	MomentUploads  int                        `json:"moment_uploads"`
	MomentTotal    int                        `json:"moment_total"`
	MomentApproved int                        `json:"moment_approved"`
	MomentPending  int                        `json:"moment_pending"`
	RSVPConfirmed  int                        `json:"rsvp_confirmed"`
	RSVPDeclined   int                        `json:"rsvp_declined"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
	Guests         GuestAnalyticsSummary      `json:"guests"`
	Performance    []PerformanceMetricSummary `json:"performance"`
}

type PerformanceMetricSummary struct {
	Route       string  `json:"route"`
	Metric      string  `json:"metric"`
	SampleCount int64   `json:"sample_count"`
	Average     float64 `json:"average"`
	Minimum     float64 `json:"minimum"`
	Maximum     float64 `json:"maximum"`
	P75         float64 `json:"p75"`
	P95         float64 `json:"p95"`
	Rating      string  `json:"rating,omitempty"`
}

type EventViewTrackingResponse struct {
	Tracked bool `json:"tracked"`
}

func sortedAnalyticsCounts(counts map[string]int) []AnalyticsCount {
	result := make([]AnalyticsCount, 0, len(counts))
	for name, value := range counts {
		result = append(result, AnalyticsCount{Name: name, Value: value})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Value == result[j].Value {
			return result[i].Name < result[j].Name
		}
		return result[i].Value > result[j].Value
	})
	return result
}

func NewGuestAnalyticsSummary(guests []AnalyticsGuest) GuestAnalyticsSummary {
	result := GuestAnalyticsSummary{
		Dietary: []AnalyticsCount{}, Methods: []AnalyticsCount{}, Roles: []AnalyticsCount{},
		Tables: []AnalyticsCount{}, Timeline: []AnalyticsTimelinePoint{}, TopCompanions: []AnalyticsTopCompanion{},
	}
	dietary, methods, roles, tables := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	timeline := map[string]*AnalyticsTimelinePoint{}
	for _, guest := range guests {
		result.TotalGuests++
		role := strings.TrimSpace(guest.Role)
		roles[role]++
		if table := strings.TrimSpace(guest.TableName); table != "" {
			tables[table]++
		}
		status := strings.ToUpper(strings.TrimSpace(guest.RSVPStatus))
		switch status {
		case "CONFIRMED":
			result.Confirmed++
			partySize := guest.GuestsCount
			if partySize < 1 {
				partySize = 1
			}
			companions := partySize - 1
			result.TotalCompanions += companions
			result.EstimatedAttendees += partySize
			diet := strings.TrimSpace(guest.DietaryRestrictions)
			if diet == "" {
				diet = "Ninguna"
			}
			dietary[diet]++
			if companions > 0 {
				result.TopCompanions = append(result.TopCompanions, AnalyticsTopCompanion{ID: guest.ID, FirstName: guest.FirstName, LastName: guest.LastName, Role: guest.Role, CompanionCount: companions})
			}
		case "DECLINED":
			result.Declined++
		default:
			result.Pending++
			continue
		}
		methods[strings.TrimSpace(guest.RSVPMethod)]++
		if guest.RSVPAt != nil {
			day := guest.RSVPAt.Format("2006-01-02")
			point := timeline[day]
			if point == nil {
				point = &AnalyticsTimelinePoint{Date: day}
				timeline[day] = point
			}
			switch status {
			case "CONFIRMED":
				point.Confirmed++
			case "DECLINED":
				point.Declined++
			}
		}
	}
	sort.Slice(result.TopCompanions, func(i, j int) bool {
		return result.TopCompanions[i].CompanionCount > result.TopCompanions[j].CompanionCount
	})
	if len(result.TopCompanions) > 5 {
		result.TopCompanions = result.TopCompanions[:5]
	}
	days := make([]string, 0, len(timeline))
	for day := range timeline {
		days = append(days, day)
	}
	sort.Strings(days)
	confirmed, declined := 0, 0
	for _, day := range days {
		point := timeline[day]
		confirmed += point.Confirmed
		declined += point.Declined
		point.Confirmed = confirmed
		point.Declined = declined
		result.Timeline = append(result.Timeline, *point)
	}
	result.Dietary, result.Methods, result.Roles, result.Tables = sortedAnalyticsCounts(dietary), sortedAnalyticsCounts(methods), sortedAnalyticsCounts(roles), sortedAnalyticsCounts(tables)
	return result
}

func NewEventAnalyticsResponse(analytics *models.EventAnalytics) EventAnalyticsResponse {
	if analytics == nil {
		return EventAnalyticsResponse{}
	}

	return EventAnalyticsResponse{
		ID:             analytics.ID,
		EventID:        analytics.EventID,
		Views:          analytics.Views,
		MomentComments: analytics.MomentComments,
		MomentUploads:  analytics.MomentUploads,
		MomentTotal:    analytics.MomentTotal,
		MomentApproved: analytics.MomentApproved,
		MomentPending:  analytics.MomentPending,
		RSVPConfirmed:  analytics.RSVPConfirmed,
		RSVPDeclined:   analytics.RSVPDeclined,
		CreatedAt:      analytics.CreatedAt,
		UpdatedAt:      analytics.UpdatedAt,
	}
}

func NewEventAnalyticsResponses(analytics []models.EventAnalytics) []EventAnalyticsResponse {
	response := make([]EventAnalyticsResponse, 0, len(analytics))
	for i := range analytics {
		response = append(response, NewEventAnalyticsResponse(&analytics[i]))
	}
	return response
}
