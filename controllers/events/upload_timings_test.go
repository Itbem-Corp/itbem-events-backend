package events

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"events-stocks/models"

	eventsService "events-stocks/services/events"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeUploadTimings(t *testing.T, raw string) uploadTimingPayload {
	t.Helper()
	var p uploadTimingPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	return p
}

func TestNormalizeUploadTimingsKeepsOnlyAllowListedValues(t *testing.T) {
	p := decodeUploadTimings(t, `{"platform":"ios","iosSafari":true,"kind":"heic","sizeBytes":123456,"mimeType":"image/heic","multipart":false,"outcome":"done","filename":"x.heic","phasesMs":{"preview":120,"optimize":-1,"presign":80,"put":3000,"confirm":700000,"bogus":5},"totalMs":4000}`)
	kind, platform, outcome, samples, ok := normalizeUploadTimings(p)
	require.True(t, ok)
	assert.Equal(t, "heic", kind)
	assert.Equal(t, "ios_safari", platform)
	assert.Equal(t, "done", outcome)
	got := map[string]float64{}
	for _, s := range samples {
		got[s.phase] = s.value
	}
	assert.Equal(t, map[string]float64{"preview": 120, "presign": 80, "put": 3000, "total": 4000}, got)
}

func TestNormalizeUploadTimingsRejectsInvalid(t *testing.T) {
	for name, raw := range map[string]string{
		"kind":     `{"platform":"ios","kind":"gif","outcome":"done","totalMs":10}`,
		"platform": `{"platform":"windows","kind":"photo","outcome":"done","totalMs":10}`,
		"outcome":  `{"platform":"ios","kind":"photo","outcome":"maybe","totalMs":10}`,
		"nosample": `{"platform":"ios","kind":"photo","outcome":"done","phasesMs":{"put":-5}}`,
	} {
		_, _, _, _, ok := normalizeUploadTimings(decodeUploadTimings(t, raw))
		assert.False(t, ok, name)
	}
}

func TestUploadTimingPlatformDerivation(t *testing.T) {
	for _, tc := range []struct {
		platform string
		safari   bool
		want     string
	}{{"ios", true, "ios_safari"}, {"ios", false, "ios_other"}, {"android", true, "android"}, {"desktop", false, "desktop"}} {
		got, ok := uploadTimingPlatform(tc.platform, tc.safari)
		assert.True(t, ok)
		assert.Equal(t, tc.want, got)
	}
}

func TestUploadTimingBucket(t *testing.T) {
	idx, upper := uploadTimingBucket(0)
	assert.Equal(t, 0, idx)
	assert.Equal(t, float64(50), upper)
	idx, upper = uploadTimingBucket(251)
	assert.Equal(t, 3, idx)
	assert.Equal(t, float64(500), upper)
	idx, upper = uploadTimingBucket(600000)
	assert.Equal(t, 11, idx)
	assert.Equal(t, float64(600000), upper)
}

func TestSummarizeUploadTimingsP50P95(t *testing.T) {
	rows := []models.EventUploadTimingDaily{
		{Phase: "put", Kind: "photo", Platform: "ios_safari", Outcome: "done", BucketIndex: 4, UpperBound: 1000, SampleCount: 50},
		{Phase: "put", Kind: "photo", Platform: "ios_safari", Outcome: "error", BucketIndex: 4, UpperBound: 1000, SampleCount: 10},
		{Phase: "put", Kind: "photo", Platform: "ios_safari", Outcome: "done", BucketIndex: 5, UpperBound: 2500, SampleCount: 30},
		{Phase: "put", Kind: "photo", Platform: "ios_safari", Outcome: "done", BucketIndex: 6, UpperBound: 5000, SampleCount: 10},
		{Phase: "put", Kind: "photo", Platform: "android", BucketIndex: 0, UpperBound: 50, SampleCount: 4},
	}
	out := summarizeUploadTimings(rows)
	require.Len(t, out, 2)
	assert.Equal(t, "android", out[0].Platform)
	assert.Equal(t, int64(4), out[0].SampleCount)
	assert.Equal(t, "ios_safari", out[1].Platform)
	assert.Equal(t, int64(100), out[1].SampleCount)
	assert.Equal(t, float64(1000), out[1].P50)
	assert.Equal(t, float64(5000), out[1].P95)
}

func TestUploadTimingModelStoresNoPII(t *testing.T) {
	typ := reflect.TypeOf(models.EventUploadTimingDaily{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		for _, banned := range []string{"IP", "UserAgent", "FileName", "Filename", "Mime", "Size", "Guest"} {
			assert.NotContains(t, name, banned)
		}
	}
}

func TestTrackUploadTimingsRejectsInvalidPayload(t *testing.T) {
	c, rec := newEchoCtx(http.MethodPost, "/api/events/boda-demo/upload-timings", `{"platform":"ios","kind":"gif","outcome":"done","totalMs":10}`)
	c.SetParamNames("identifier")
	c.SetParamValues("boda-demo")
	require.NoError(t, TrackUploadTimings(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTrackUploadTimingsUnknownEventUsesNonEnumeratingContract(t *testing.T) {
	restoreEventSvc := eventSvc
	t.Cleanup(func() { eventSvc = restoreEventSvc })
	eventSvc = eventsService.NewEventService(&mockEventsRepo{
		GetEventByIdentifierFunc: func(identifier string) (*models.Event, error) {
			return nil, errors.New("not found")
		},
	}, &mockCacheRepo{})

	c, rec := newEchoCtx(http.MethodPost, "/api/events/missing-event/upload-timings", `{"platform":"ios","iosSafari":true,"kind":"photo","outcome":"done","phasesMs":{"put":900},"totalMs":1200}`)
	c.SetParamNames("identifier")
	c.SetParamValues("missing-event")
	require.NoError(t, TrackUploadTimings(c))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Body.String(), `"accepted":false`)
}
