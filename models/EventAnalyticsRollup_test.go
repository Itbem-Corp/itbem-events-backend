package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventAnalyticsRollupUsesSharedWorkerTable(t *testing.T) {
	assert.Equal(t, "event_analytics_rollups", (EventAnalyticsRollup{}).TableName())
}
