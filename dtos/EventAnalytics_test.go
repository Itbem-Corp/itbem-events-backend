package dtos

import (
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestNewGuestAnalyticsSummaryPreservesDashboardMetrics(t *testing.T) {
	dayOne := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	dayTwo := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	guests := []AnalyticsGuest{
		{ID: uuid.Must(uuid.NewV4()), FirstName: "Ana", Role: "vip", RSVPStatus: "confirmed", RSVPAt: &dayOne, RSVPMethod: "web", GuestsCount: 3, DietaryRestrictions: "Vegana", TableName: "Mesa 1"},
		{ID: uuid.Must(uuid.NewV4()), FirstName: "Luis", Role: "guest", RSVPStatus: "declined", RSVPAt: &dayTwo, RSVPMethod: "host"},
		{ID: uuid.Must(uuid.NewV4()), FirstName: "Mara", Role: "guest", RSVPStatus: "pending", GuestsCount: 1},
	}

	summary := NewGuestAnalyticsSummary(guests)
	if summary.TotalGuests != 3 || summary.Confirmed != 1 || summary.Declined != 1 || summary.Pending != 1 {
		t.Fatalf("unexpected RSVP totals: %+v", summary)
	}
	if summary.TotalCompanions != 2 || summary.EstimatedAttendees != 3 {
		t.Fatalf("unexpected attendance totals: %+v", summary)
	}
	if len(summary.Timeline) != 2 || summary.Timeline[1].Confirmed != 1 || summary.Timeline[1].Declined != 1 {
		t.Fatalf("unexpected cumulative timeline: %+v", summary.Timeline)
	}
	if len(summary.TopCompanions) != 1 || summary.TopCompanions[0].CompanionCount != 2 {
		t.Fatalf("unexpected companion ranking: %+v", summary.TopCompanions)
	}
}
