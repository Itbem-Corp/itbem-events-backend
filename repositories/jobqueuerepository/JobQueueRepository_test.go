package jobqueuerepository

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationPublishesRuntimeFixtureWhenConfigured(t *testing.T) {
	contractPath, queue := os.Getenv("EVENTIAPP_RUNTIME_CONTRACT"), os.Getenv("EVENTIAPP_INTEGRATION_QUEUE_URL")
	if contractPath == "" || queue == "" { t.Skip("integration queue is not configured") }
	contents, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	var contract struct { WorkerJobs []struct { Envelope json.RawMessage `json:"envelope"` } `json:"workerJobs"` }
	require.NoError(t, json.Unmarshal(contents, &contract))
	require.NotEmpty(t, contract.WorkerJobs)

	client, queueURL, once = nil, "", sync.Once{}
	t.Cleanup(func() { client, queueURL, once = nil, "", sync.Once{} })
	Init("us-east-2", "test", "test", queue)
	require.NoError(t, PublishRaw(string(contract.WorkerJobs[0].Envelope), "itbem"))
}

func TestRuntimeContractAnalyticsFixtureIsAcceptedByAPIPublisher(t *testing.T) {
	contractPath := os.Getenv("EVENTIAPP_RUNTIME_CONTRACT")
	if contractPath == "" {
		t.Skip("runtime contract fixture is only required by the cross-service gate")
	}

	contents, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	var contract struct {
		WorkerJobs []struct {
			Name     string          `json:"name"`
			Envelope json.RawMessage `json:"envelope"`
		} `json:"workerJobs"`
	}
	require.NoError(t, json.Unmarshal(contents, &contract))
	require.NotEmpty(t, contract.WorkerJobs)

	fixture := contract.WorkerJobs[0]
	assert.Equal(t, "analytics-rollup-v2", fixture.Name)
	var envelope analyticsRollupEnvelope
	require.NoError(t, json.Unmarshal(fixture.Envelope, &envelope))
	assert.Equal(t, jobSchemaVersion, envelope.SchemaVersion)
	assert.Equal(t, analyticsJobType, envelope.Type)
	assert.NotEqual(t, uuid.Nil, envelope.JobID)
	assert.Equal(t, "itbem", envelope.TenantCode)
	assert.Equal(t, "analytics_read", envelope.Payload.Trigger)

	serialized, err := json.Marshal(envelope)
	require.NoError(t, err)
	assert.JSONEq(t, string(fixture.Envelope), string(serialized))
}

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

func TestBuildAnalyticsRollupMessageReturnsPersistableDeterministicEnvelope(t *testing.T) {
	eventID := uuid.Must(uuid.FromString("7c9e6679-7425-40de-944b-e07fc1f90ae7"))
	now := time.Date(2026, 7, 16, 17, 5, 42, 0, time.UTC)

	body, dedupeKey, tenant, err := BuildAnalyticsRollupMessage(eventID, " ItBeM ", now, "request-123")
	require.NoError(t, err)
	assert.Equal(t, "itbem", tenant)

	envelope := buildAnalyticsRollupEnvelope(eventID, tenant, now)
	assert.Equal(t, envelope.JobID.String(), dedupeKey)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(body), &decoded))
	assert.Equal(t, dedupeKey, decoded["job_id"])
	assert.Equal(t, analyticsJobType, decoded["type"])
	assert.Equal(t, "request-123", decoded["correlation_id"])
}
