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

func TestHistogramPercentileUsesCumulativeBucketCounts(t *testing.T) {
	buckets := map[int]models.EventPerformanceBucketDaily{
		0: {BucketIndex: 0, UpperBound: 1000, SampleCount: 50},
		1: {BucketIndex: 1, UpperBound: 2500, SampleCount: 30},
		2: {BucketIndex: 2, UpperBound: 4000, SampleCount: 20},
	}

	assert.Equal(t, float64(2500), histogramPercentile(buckets, 100, 0.75))
	assert.Equal(t, float64(4000), histogramPercentile(buckets, 100, 0.95))
}

func TestPerformanceRatingUsesCoreWebVitalsP75Thresholds(t *testing.T) {
	assert.Equal(t, "good", performanceRating("lcp", 2500))
	assert.Equal(t, "needs_improvement", performanceRating("inp", 300))
	assert.Equal(t, "poor", performanceRating("cls", 0.4))
	assert.Empty(t, performanceRating("page_spec_ms", 800))
}

func TestPerformanceBucketUsesMetricSpecificBounds(t *testing.T) {
	index, upper := performanceBucket("cls", 0.08)
	assert.Equal(t, 1, index)
	assert.Equal(t, 0.1, upper)

	index, upper = performanceBucket("lcp", 2600)
	assert.Equal(t, 4, index)
	assert.Equal(t, float64(4000), upper)
}
