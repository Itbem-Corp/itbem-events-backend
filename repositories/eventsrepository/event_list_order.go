package eventsrepository

import (
	"strings"

	"events-stocks/dtos"
)

// eventListOrder sorts the dashboard event page (EVT-15): soonest first for
// the forward-looking filters ("upcoming", "today"), most recent first for
// "past" and "all". events.id breaks ties so pagination is stable.
func eventListOrder(query dtos.EventListQuery) string {
	switch strings.ToLower(strings.TrimSpace(query.Filter)) {
	case "upcoming", "today":
		return "events.event_date_time ASC, events.id ASC"
	default:
		return "events.event_date_time DESC, events.id DESC"
	}
}
