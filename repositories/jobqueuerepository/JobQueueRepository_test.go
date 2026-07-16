package jobqueuerepository

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAnalyticsRollupEnvelopeMatchesRustContractAndDeduplicatesMinute(t *testing.T) {
	eventID := uuid.Must(uuid.FromString("7c9e6679-7425-40de-944b-e07fc1f90ae7"))
	first := buildAnalyticsRollupEnvelope(eventID, "itbem", time.Date(2026, 7, 12, 18, 0, 1, 0, time.UTC))
	second := buildAnalyticsRollupEnvelope(eventID, "itbem", time.Date(2026, 7, 12, 18, 0, 59, 0, time.UTC))
	nextMinute := buildAnalyticsRollupEnvelope(eventID, "itbem", time.Date(2026, 7, 12, 18, 1, 0, 0, time.UTC))

	assert.Equal(t, first.JobID, second.JobID)
	assert.NotEqual(t, first.JobID, nextMinute.JobID)
	payload, err := json.Marshal(first)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"schema_version":2,
		"job_id":"`+first.JobID.String()+`",
		"occurred_at":"2026-07-12T18:00:01Z",
		"tenant_code":"itbem",
		"type":"analytics.rollup",
		"payload":{
			"event_id":"7c9e6679-7425-40de-944b-e07fc1f90ae7",
			"requested_at":"2026-07-12T18:00:01Z",
			"trigger":"analytics_read"
		}
	}`, string(payload))
}

func TestPublishAnalyticsRollupIsNoopWithoutConfiguredQueue(t *testing.T) {
	enqueued, err := PublishAnalyticsRollup(uuid.Must(uuid.NewV4()))
	require.NoError(t, err)
	assert.False(t, enqueued)
}

func TestReserveAnalyticsPublishDeduplicatesCurrentBucketAndPrunesOlderEvents(t *testing.T) {
	publishMu.Lock()
	original := publishedBuckets
	publishedBuckets = map[uuid.UUID]time.Time{}
	publishMu.Unlock()
	t.Cleanup(func() {
		publishMu.Lock()
		publishedBuckets = original
		publishMu.Unlock()
	})

	bucket := time.Date(2026, 7, 12, 18, 1, 0, 0, time.UTC)
	staleEventID := uuid.Must(uuid.NewV4())
	currentEventID := uuid.Must(uuid.NewV4())
	publishMu.Lock()
	publishedBuckets[staleEventID] = bucket.Add(-analyticsJobBucket)
	publishMu.Unlock()

	assert.True(t, reserveAnalyticsPublish(currentEventID, bucket))
	assert.False(t, reserveAnalyticsPublish(currentEventID, bucket))
	publishMu.Lock()
	assert.NotContains(t, publishedBuckets, staleEventID)
	assert.Equal(t, bucket, publishedBuckets[currentEventID])
	publishMu.Unlock()
}
