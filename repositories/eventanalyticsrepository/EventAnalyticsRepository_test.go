package eventanalyticsrepository

import (
	"events-stocks/models"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAnalyticsCounterColumnSupportsMomentComments(t *testing.T) {
	column, ok := analyticsCounterColumn("moment_comments")

	assert.True(t, ok)
	assert.Equal(t, "moment_comments", column)
}

func TestSetAnalyticsCounterSupportsMomentComments(t *testing.T) {
	analytics := &models.EventAnalytics{}

	setAnalyticsCounter(analytics, "moment_comments", 5)

	assert.Equal(t, 5, analytics.MomentComments)
}
