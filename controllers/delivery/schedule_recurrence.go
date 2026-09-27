package delivery

import (
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
)

type deliveryScheduleRecurrence struct {
	Frequency     string   `json:"frequency"`
	Interval      int      `json:"interval"`
	Weekdays      []string `json:"weekdays,omitempty"`
	MonthDay      int      `json:"month_day,omitempty"`
	LocalTime     string   `json:"local_time"`
	TimeZone      string   `json:"time_zone"`
	StartDate     string   `json:"starts_on"`
	EndDate       string   `json:"ends_on,omitempty"`
	MisfirePolicy string   `json:"misfire_policy"`
}

type deliveryScheduleTemplate struct {
	Title                     string   `json:"title"`
	Description               string   `json:"description,omitempty"`
	ExpectedOutcome           string   `json:"expected_outcome"`
	ContextSourceIDs          []string `json:"context_source_ids"`
	PrimaryRepositorySourceID string   `json:"primary_repository_source_id,omitempty"`
	AssignedAgent             string   `json:"assigned_agent,omitempty"`
	IncludedScope             []string `json:"included_scope,omitempty"`
	ExcludedScope             []string `json:"excluded_scope,omitempty"`
	AcceptanceCriteria        []string `json:"acceptance_criteria,omitempty"`
	BudgetMicros              int64    `json:"budget_microusd"`
	BudgetAlertPercent        int      `json:"budget_alert_percent,omitempty"`
	MaxConcurrency            *int     `json:"max_concurrency,omitempty"`
}

type deliveryScheduleCreateRequest struct {
	Name       string                     `json:"name"`
	Template   deliveryScheduleTemplate   `json:"template"`
	Recurrence deliveryScheduleRecurrence `json:"recurrence"`
}

type deliverySchedulePatchRequest struct {
	Name       *string                     `json:"name,omitempty"`
	Template   *deliveryScheduleTemplate   `json:"template,omitempty"`
	Recurrence *deliveryScheduleRecurrence `json:"recurrence,omitempty"`
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func normalizeDeliveryScheduleRecurrence(rule deliveryScheduleRecurrence) (deliveryScheduleRecurrence, *time.Location, error) {
	rule.Frequency = strings.ToLower(strings.TrimSpace(rule.Frequency))
	rule.TimeZone = strings.TrimSpace(rule.TimeZone)
	rule.LocalTime = strings.TrimSpace(rule.LocalTime)
	rule.StartDate = strings.TrimSpace(rule.StartDate)
	rule.EndDate = strings.TrimSpace(rule.EndDate)
	rule.MisfirePolicy = strings.ToLower(strings.TrimSpace(rule.MisfirePolicy))
	if rule.Interval == 0 {
		rule.Interval = 1
	}
	if rule.MisfirePolicy == "" {
		rule.MisfirePolicy = "coalesce"
	}
	if rule.MisfirePolicy != "coalesce" {
		return rule, nil, fmt.Errorf("misfire_policy must be coalesce")
	}
	location, err := time.LoadLocation(rule.TimeZone)
	if err != nil || rule.TimeZone == "Local" {
		return rule, nil, fmt.Errorf("time_zone must be a valid IANA timezone")
	}
	if _, err := time.Parse("15:04", rule.LocalTime); err != nil || len(rule.LocalTime) != 5 {
		return rule, nil, fmt.Errorf("local_time must use HH:MM 24-hour format")
	}
	startDate, err := parseScheduleDate(rule.StartDate)
	if err != nil {
		return rule, nil, fmt.Errorf("starts_on must be a valid YYYY-MM-DD date")
	}
	var endDate time.Time
	if rule.EndDate != "" {
		endDate, err = parseScheduleDate(rule.EndDate)
		if err != nil || endDate.Before(startDate) {
			return rule, nil, fmt.Errorf("ends_on must be a valid local date on or after starts_on")
		}
	}
	switch rule.Frequency {
	case "daily":
		if rule.Interval < 1 || rule.Interval > 365 || len(rule.Weekdays) != 0 || rule.MonthDay != 0 {
			return rule, nil, fmt.Errorf("daily recurrence requires interval 1..365 and no weekdays or month_day")
		}
	case "weekly":
		if rule.Interval < 1 || rule.Interval > 52 || len(rule.Weekdays) == 0 || rule.MonthDay != 0 {
			return rule, nil, fmt.Errorf("weekly recurrence requires interval 1..52 and at least one weekday")
		}
		seen := make(map[string]struct{}, len(rule.Weekdays))
		weekdays := make([]string, 0, len(rule.Weekdays))
		for _, day := range rule.Weekdays {
			day = strings.ToLower(strings.TrimSpace(day))
			if _, ok := weekdayNames[day]; !ok {
				return rule, nil, fmt.Errorf("weekdays may contain only sun, mon, tue, wed, thu, fri, sat")
			}
			if _, exists := seen[day]; !exists {
				seen[day] = struct{}{}
				weekdays = append(weekdays, day)
			}
		}
		sort.Slice(weekdays, func(i, j int) bool { return weekdayNames[weekdays[i]] < weekdayNames[weekdays[j]] })
		rule.Weekdays = weekdays
	case "monthly":
		if rule.Interval < 1 || rule.Interval > 24 || rule.MonthDay < 1 || rule.MonthDay > 31 || len(rule.Weekdays) != 0 {
			return rule, nil, fmt.Errorf("monthly recurrence requires interval 1..24 and month_day 1..31")
		}
	default:
		return rule, nil, fmt.Errorf("frequency must be daily, weekly, or monthly")
	}
	return rule, location, nil
}

func parseScheduleDate(raw string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", raw)
	if err != nil || parsed.Format("2006-01-02") != raw {
		return time.Time{}, fmt.Errorf("invalid date")
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC), nil
}

// nextDeliveryScheduleOccurrence returns the next occurrence strictly after
// after. A monthly day that does not exist in a month clamps to that month's
// final day. A nonexistent wall time during a spring DST gap is skipped; an
// ambiguous wall time during a fall transition is emitted once at its earlier
// UTC instant.
func nextDeliveryScheduleOccurrence(rule deliveryScheduleRecurrence, after time.Time) (*time.Time, string, error) {
	rule, location, err := normalizeDeliveryScheduleRecurrence(rule)
	if err != nil {
		return nil, "", err
	}
	start, _ := parseScheduleDate(rule.StartDate)
	var end time.Time
	if rule.EndDate != "" {
		end, _ = parseScheduleDate(rule.EndDate)
	}
	localAfter := after.In(location)
	date := time.Date(localAfter.Year(), localAfter.Month(), localAfter.Day(), 0, 0, 0, 0, time.UTC)
	if date.Before(start) {
		date = start
	}
	hour, minute := strconvScheduleClock(rule.LocalTime)
	for days := 0; days < 366*30; days++ {
		if !end.IsZero() && date.After(end) {
			return nil, "", nil
		}
		if scheduleDateMatches(rule, start, date) {
			instant, ok := resolveScheduleWallClock(date, hour, minute, location)
			if ok && instant.After(after) {
				return &instant, instant.In(location).Format("2006-01-02T15:04"), nil
			}
		}
		date = date.AddDate(0, 0, 1)
	}
	return nil, "", fmt.Errorf("no schedule occurrence found within the 30-year calculation horizon")
}

func strconvScheduleClock(value string) (int, int) {
	parsed, _ := time.Parse("15:04", value)
	return parsed.Hour(), parsed.Minute()
}

func scheduleDateMatches(rule deliveryScheduleRecurrence, start, date time.Time) bool {
	switch rule.Frequency {
	case "daily":
		return int(date.Sub(start).Hours()/24)%rule.Interval == 0
	case "weekly":
		startWeek := start.AddDate(0, 0, -((int(start.Weekday()) + 6) % 7))
		dateWeek := date.AddDate(0, 0, -((int(date.Weekday()) + 6) % 7))
		weeks := int(dateWeek.Sub(startWeek).Hours() / (24 * 7))
		if weeks < 0 || weeks%rule.Interval != 0 {
			return false
		}
		day := "sun"
		for candidate, weekday := range weekdayNames {
			if weekday == date.Weekday() {
				day = candidate
				break
			}
		}
		for _, configured := range rule.Weekdays {
			if configured == day {
				return true
			}
		}
		return false
	case "monthly":
		months := (date.Year()-start.Year())*12 + int(date.Month()-start.Month())
		if months < 0 || months%rule.Interval != 0 {
			return false
		}
		lastDay := time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
		wanted := rule.MonthDay
		if wanted > lastDay {
			wanted = lastDay
		}
		return date.Day() == wanted
	default:
		return false
	}
}

func resolveScheduleWallClock(date time.Time, hour, minute int, location *time.Location) (time.Time, bool) {
	wallAsUTC := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, time.UTC)
	offsets := make(map[int]struct{})
	for delta := -72; delta <= 72; delta += 6 {
		_, offset := wallAsUTC.Add(time.Duration(delta) * time.Hour).In(location).Zone()
		offsets[offset] = struct{}{}
	}
	candidates := make([]time.Time, 0, 2)
	for offset := range offsets {
		candidate := wallAsUTC.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if local.Year() == date.Year() && local.Month() == date.Month() && local.Day() == date.Day() && local.Hour() == hour && local.Minute() == minute {
			candidates = append(candidates, candidate.UTC())
		}
	}
	if len(candidates) == 0 {
		return time.Time{}, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	return candidates[0], true
}
