package events

import (
	"bytes"
	"sort"
	"strings"

	"events-stocks/models"
)

// sortEventListPage applies the dashboard list order in the in-memory
// fallback, matching eventsrepository.eventListOrder (EVT-15): soonest first
// for "upcoming"/"today", most recent first otherwise, id as tiebreaker.
func sortEventListPage(events []models.Event, filter string) {
	ascending := false
	switch strings.ToLower(strings.TrimSpace(filter)) {
	case "upcoming", "today":
		ascending = true
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.EventDateTime.Equal(b.EventDateTime) {
			if ascending {
				return a.EventDateTime.Before(b.EventDateTime)
			}
			return a.EventDateTime.After(b.EventDateTime)
		}
		cmp := bytes.Compare(a.ID.Bytes(), b.ID.Bytes())
		if ascending {
			return cmp < 0
		}
		return cmp > 0
	})
}
