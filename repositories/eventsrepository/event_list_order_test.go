package eventsrepository

import (
	"testing"

	"events-stocks/dtos"

	"github.com/stretchr/testify/assert"
)

// EVT-15: "Próximos" showed the farthest event first and the order had no
// tiebreaker.
func TestEventListOrder(t *testing.T) {
	asc := "events.event_date_time ASC, events.id ASC"
	desc := "events.event_date_time DESC, events.id DESC"
	assert.Equal(t, asc, eventListOrder(dtos.EventListQuery{Filter: "upcoming"}))
	assert.Equal(t, asc, eventListOrder(dtos.EventListQuery{Filter: " TODAY "}))
	assert.Equal(t, desc, eventListOrder(dtos.EventListQuery{Filter: "past"}))
	assert.Equal(t, desc, eventListOrder(dtos.EventListQuery{Filter: "all"}))
	assert.Equal(t, desc, eventListOrder(dtos.EventListQuery{}))
}
