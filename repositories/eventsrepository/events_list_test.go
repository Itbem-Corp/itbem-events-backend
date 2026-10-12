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

// EVT-10: the dashboard search ignores accents and letter case.
func TestFoldSearchTextMatchesColumnFold(t *testing.T) {
	assert.Equal(t, "nandu", foldSearchText("Ñandú"))
	assert.Equal(t, "jose maria", foldSearchText("JOSÉ MARÍA"))
	assert.Equal(t, "quinceanera", foldSearchText("Quinceañera"))
	assert.Equal(t, len([]rune(searchFoldFrom)), len([]rune(searchFoldTo)))
}
