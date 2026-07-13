package eventsrepository

import (
	"events-stocks/dtos"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventListNeedsFilteredTotal(t *testing.T) {
	tests := []struct {
		name  string
		query dtos.EventListQuery
		want  bool
	}{
		{name: "default list reuses all count", query: dtos.EventListQuery{Filter: "all"}, want: false},
		{name: "empty filter reuses all count", query: dtos.EventListQuery{}, want: false},
		{name: "normalized all filter reuses all count", query: dtos.EventListQuery{Filter: " ALL "}, want: false},
		{name: "search needs filtered total", query: dtos.EventListQuery{Filter: "all", Search: "boda"}, want: true},
		{name: "date filter needs filtered total", query: dtos.EventListQuery{Filter: "upcoming"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, eventListNeedsFilteredTotal(tt.query))
		})
	}
}
