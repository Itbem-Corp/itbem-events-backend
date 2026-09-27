package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid"
)

func TestDeliveryScheduleRecurrenceDailyAndCoalesce(t *testing.T) {
	rule := deliveryScheduleRecurrence{
		Frequency: "daily", Interval: 1, LocalTime: "09:30", TimeZone: "America/Mexico_City",
		StartDate: "2026-09-25", MisfirePolicy: "coalesce",
	}
	after := time.Date(2026, 9, 25, 15, 29, 0, 0, time.UTC) // 09:29 local
	next, local, err := nextDeliveryScheduleOccurrence(rule, after)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 25, 15, 30, 0, 0, time.UTC)
	if next == nil || !next.Equal(want) || local != "2026-09-25T09:30" {
		t.Fatalf("next daily occurrence = %v (%q), want %s (local 09:30)", next, local, want)
	}
	// Coalescing several missed daily ticks advances to the first future tick,
	// rather than replaying a backlog.
	next, local, err = nextDeliveryScheduleOccurrence(rule, time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || local != "2026-09-29T09:30" {
		t.Fatalf("coalesced next occurrence = %v (%q), want Sep 29 at 09:30 local", next, local)
	}
}

func TestDeliveryScheduleRecurrenceWeeklyAndMonthlyCalendarRules(t *testing.T) {
	weekly := deliveryScheduleRecurrence{
		Frequency: "weekly", Interval: 1, Weekdays: []string{"wed", "mon"}, LocalTime: "08:15",
		TimeZone: "UTC", StartDate: "2026-09-25", MisfirePolicy: "coalesce",
	}
	next, local, err := nextDeliveryScheduleOccurrence(weekly, time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || local != "2026-09-28T08:15" {
		t.Fatalf("weekly next occurrence = %v (%q), want Monday Sep 28", next, local)
	}

	monthly := deliveryScheduleRecurrence{
		Frequency: "monthly", Interval: 1, MonthDay: 31, LocalTime: "10:00",
		TimeZone: "UTC", StartDate: "2026-01-31", MisfirePolicy: "coalesce",
	}
	next, local, err = nextDeliveryScheduleOccurrence(monthly, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || local != "2026-02-28T10:00" {
		t.Fatalf("monthly day-31 occurrence = %v (%q), want clamped Feb 28", next, local)
	}
}

func TestDeliveryScheduleRecurrenceHandlesDSTGapsAndAmbiguity(t *testing.T) {
	daily := deliveryScheduleRecurrence{
		Frequency: "daily", Interval: 1, LocalTime: "02:30", TimeZone: "America/New_York",
		StartDate: "2026-03-08", MisfirePolicy: "coalesce",
	}
	// New York skips 02:30 on the spring transition date; the schedule skips
	// that impossible wall-clock occurrence and resumes the next day.
	next, local, err := nextDeliveryScheduleOccurrence(daily, time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || local != "2026-03-09T02:30" {
		t.Fatalf("DST-gap occurrence = %v (%q), want Mar 9 02:30 local", next, local)
	}

	fall := daily
	fall.LocalTime = "01:30"
	fall.StartDate = "2026-11-01"
	next, local, err = nextDeliveryScheduleOccurrence(fall, time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC) // earlier EDT instance
	if next == nil || !next.Equal(want) || local != "2026-11-01T01:30" {
		t.Fatalf("ambiguous fall occurrence = %v (%q), want first 01:30 at %s", next, local, want)
	}
}

func TestNormalizeDeliveryScheduleRecurrenceRejectsUnboundedOrNonAllowlistedValues(t *testing.T) {
	valid := deliveryScheduleRecurrence{Frequency: "daily", Interval: 1, LocalTime: "09:00", TimeZone: "UTC", StartDate: "2026-09-25", MisfirePolicy: "coalesce"}
	for name, mutate := range map[string]func(*deliveryScheduleRecurrence){
		"frequency": func(rule *deliveryScheduleRecurrence) { rule.Frequency = "hourly" },
		"timezone":  func(rule *deliveryScheduleRecurrence) { rule.TimeZone = "Local" },
		"clock":     func(rule *deliveryScheduleRecurrence) { rule.LocalTime = "9:00" },
		"misfire":   func(rule *deliveryScheduleRecurrence) { rule.MisfirePolicy = "catch_up_all" },
		"end date":  func(rule *deliveryScheduleRecurrence) { rule.EndDate = "2026-09-24" },
	} {
		t.Run(name, func(t *testing.T) {
			rule := valid
			mutate(&rule)
			if _, _, err := normalizeDeliveryScheduleRecurrence(rule); err == nil {
				t.Fatal("invalid recurrence was accepted")
			}
		})
	}
}

func TestRecurringWorkItemRequiresPositiveBudgetAndSelectedContext(t *testing.T) {
	projectID := uuid.Must(uuid.NewV4())
	contextID := uuid.Must(uuid.NewV4()).String()
	base := workItemRequest{Title: "Weekly report", ExpectedOutcome: "A reviewed report", ContextSourceIDs: []string{contextID}}
	if _, err := normalizeWorkItemRequest(projectID, "actor", base, true); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("zero recurring budget error = %v, want a budget validation error", err)
	}
	base.BudgetMicros = 100
	if _, err := normalizeWorkItemRequest(projectID, "actor", base, true); err != nil {
		t.Fatalf("positive recurring budget and context should normalize: %v", err)
	}
	base.ContextSourceIDs = nil
	if _, err := normalizeWorkItemRequest(projectID, "actor", base, true); err == nil || !strings.Contains(err.Error(), "context source") {
		t.Fatalf("missing context error = %v, want a context selection error", err)
	}
}

func TestDeliveryScheduleDTODoesNotExposeStoredModelOrEventDetails(t *testing.T) {
	schedule := deliveryScheduleDTO{
		Name: "Daily report", Status: "active",
		Template:   deliveryScheduleTemplate{Title: "Report", ExpectedOutcome: "Summary", BudgetMicros: 100},
		Recurrence: deliveryScheduleRecurrence{Frequency: "daily", LocalTime: "09:00", TimeZone: "UTC", StartDate: "2026-09-25", MisfirePolicy: "coalesce"},
	}
	encoded, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"TemplateJSON", "RecurrenceJSON", "DetailsJSON", "api_key", "secret", "credentials"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("schedule DTO unexpectedly exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestDeliveryScheduleOccurrenceKeyBindsRevisionAndLocalOccurrence(t *testing.T) {
	scheduleID := uuid.Must(uuid.NewV4())
	key := deliveryScheduleOccurrenceKey(scheduleID, 2, "2026-09-25T09:00", "America/Mexico_City")
	if key != deliveryScheduleOccurrenceKey(scheduleID, 2, "2026-09-25T09:00", "America/Mexico_City") {
		t.Fatal("same schedule revision and wall-clock occurrence must have a stable idempotency key")
	}
	if key == deliveryScheduleOccurrenceKey(scheduleID, 3, "2026-09-25T09:00", "America/Mexico_City") ||
		key == deliveryScheduleOccurrenceKey(scheduleID, 2, "2026-09-26T09:00", "America/Mexico_City") {
		t.Fatal("different schedule revisions or local occurrences must not collide")
	}
}
