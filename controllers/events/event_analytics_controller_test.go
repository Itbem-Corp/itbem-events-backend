package events

import (
	"testing"

	"events-stocks/models"
	"github.com/stretchr/testify/assert"
)

func TestApplyAnalyticsRollupKeepsAuthoritativeCountersAndCopiesDerivedCounts(t *testing.T) {
	analytics := &models.EventAnalytics{MomentComments: 9, MomentUploads: 3}
	rollup := &models.EventAnalyticsRollup{
		MomentTotal: 7, MomentApproved: 4, MomentPending: 2,
		MomentComments: 5, MomentUploads: 7,
	}

	applyAnalyticsRollup(analytics, rollup)

	assert.Equal(t, 7, analytics.MomentTotal)
	assert.Equal(t, 4, analytics.MomentApproved)
	assert.Equal(t, 2, analytics.MomentPending)
	assert.Equal(t, 9, analytics.MomentComments, "incremental counter must not move backwards")
	assert.Equal(t, 7, analytics.MomentUploads)
}
