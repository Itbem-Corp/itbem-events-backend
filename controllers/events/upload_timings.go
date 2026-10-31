package events

import (
	"context"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/utils"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var uploadTimingKinds = map[string]bool{"photo": true, "heic": true, "video": true, "other": true}
var uploadTimingOutcomes = map[string]bool{"done": true, "error": true}

// uploadTimingBucketBounds are the fixed upper bounds (ms) of the histogram.
var uploadTimingBucketBounds = []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 600000}

const uploadTimingMaxMs = 600000

type uploadTimingPayload struct {
	Platform  string   `json:"platform"`
	IOSSafari bool     `json:"iosSafari"`
	Kind      string   `json:"kind"`
	Outcome   string   `json:"outcome"`
	PhasesMs  struct { // closed set: unknown phases are ignored by the decoder
		Preview  *float64 `json:"preview"`
		Optimize *float64 `json:"optimize"`
		Presign  *float64 `json:"presign"`
		Put      *float64 `json:"put"`
		Confirm  *float64 `json:"confirm"`
	} `json:"phasesMs"`
	TotalMs *float64 `json:"totalMs"`
}

type uploadTimingSample struct {
	phase string
	value float64
}

// uploadTimingPlatform derives the closed platform bucket from the client hints.
func uploadTimingPlatform(platform string, iosSafari bool) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "ios":
		if iosSafari {
			return "ios_safari", true
		}
		return "ios_other", true
	case "android":
		return "android", true
	case "desktop":
		return "desktop", true
	}
	return "", false
}

func validUploadTimingValue(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= uploadTimingMaxMs
}

// normalizeUploadTimings validates the payload against allow-lists and returns
// the closed-enumeration fields plus the valid phase samples (including total).
func normalizeUploadTimings(body uploadTimingPayload) (kind, platform, outcome string, samples []uploadTimingSample, ok bool) {
	kind = strings.ToLower(strings.TrimSpace(body.Kind))
	outcome = strings.ToLower(strings.TrimSpace(body.Outcome))
	platform, platformOK := uploadTimingPlatform(body.Platform, body.IOSSafari)
	if !uploadTimingKinds[kind] || !uploadTimingOutcomes[outcome] || !platformOK {
		return "", "", "", nil, false
	}
	candidates := []struct {
		phase string
		value *float64
	}{
		{"preview", body.PhasesMs.Preview}, {"optimize", body.PhasesMs.Optimize},
		{"presign", body.PhasesMs.Presign}, {"put", body.PhasesMs.Put},
		{"confirm", body.PhasesMs.Confirm}, {"total", body.TotalMs},
	}
	for _, cand := range candidates {
		if validUploadTimingValue(cand.value) {
			samples = append(samples, uploadTimingSample{phase: cand.phase, value: *cand.value})
		}
	}
	if len(samples) == 0 {
		return "", "", "", nil, false
	}
	return kind, platform, outcome, samples, true
}

func uploadTimingBucket(value float64) (int, float64) {
	for index, upper := range uploadTimingBucketBounds {
		if value <= upper {
			return index, upper
		}
	}
	last := len(uploadTimingBucketBounds) - 1
	return last, uploadTimingBucketBounds[last]
}

// POST /api/events/:identifier/upload-timings
// Public, aggregate-only guest upload phase timings. Access mirrors
// TrackPerformance; nothing but closed enumerations and bucket counts is stored.
func TrackUploadTimings(c echo.Context) error {
	identifier := strings.TrimSpace(c.Param("identifier"))
	if identifier == "" {
		return utils.Error(c, http.StatusBadRequest, "Missing identifier", "")
	}
	var body uploadTimingPayload
	if err := c.Bind(&body); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid upload timings payload", "")
	}
	kind, platform, outcome, samples, ok := normalizeUploadTimings(body)
	if !ok {
		return utils.Error(c, http.StatusBadRequest, "Invalid upload timings payload", "")
	}
	event, err := eventSvc.GetEventByIdentifier(identifier)
	if err != nil || event == nil {
		return utils.Success(c, http.StatusAccepted, "Upload timings ignored", map[string]bool{"accepted": false})
	}
	allowed, err := allowPublicViewTracking(event, utils.PublicInvitationQueryToken(c), utils.PublicEventAccessToken(c))
	if err != nil || !allowed {
		return utils.Success(c, http.StatusAccepted, "Upload timings ignored", map[string]bool{"accepted": false})
	}
	go persistUploadTimings(event.ID, kind, platform, outcome, samples)
	return utils.Success(c, http.StatusAccepted, "Upload timings accepted", map[string]bool{"accepted": true})
}

func persistUploadTimings(eventID uuid.UUID, kind, platform, outcome string, samples []uploadTimingSample) {
	if configuration.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	bucket := time.Now().UTC().Truncate(24 * time.Hour)
	for _, sample := range samples {
		index, upper := uploadTimingBucket(sample.value)
		row := models.EventUploadTimingDaily{EventID: eventID, BucketDate: bucket, Phase: sample.phase, Kind: kind, Platform: platform, Outcome: outcome, BucketIndex: index, UpperBound: upper, SampleCount: 1}
		if err := configuration.DB.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "event_id"}, {Name: "bucket_date"}, {Name: "phase"}, {Name: "kind"}, {Name: "platform"}, {Name: "outcome"}, {Name: "bucket_index"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"sample_count": gorm.Expr("event_upload_timing_dailies.sample_count + 1"),
				"updated_at":   gorm.Expr("NOW()"),
			}),
		}).Create(&row).Error; err != nil {
			slog.Warn("failed to persist upload timing aggregate", "event_id", eventID, "phase", sample.phase, "error", err)
		}
	}
}

type uploadTimingSummaryKey struct{ phase, kind, platform string }

// summarizeUploadTimings folds histogram rows into p50/p95 per phase, kind and platform.
func summarizeUploadTimings(rows []models.EventUploadTimingDaily) []dtos.UploadTimingSummary {
	type acc struct {
		total   int64
		buckets map[int]models.EventPerformanceBucketDaily
	}
	accs := map[uploadTimingSummaryKey]*acc{}
	for _, row := range rows {
		key := uploadTimingSummaryKey{row.Phase, row.Kind, row.Platform}
		a := accs[key]
		if a == nil {
			a = &acc{buckets: map[int]models.EventPerformanceBucketDaily{}}
			accs[key] = a
		}
		b := a.buckets[row.BucketIndex]
		b.BucketIndex, b.UpperBound = row.BucketIndex, row.UpperBound
		b.SampleCount += row.SampleCount
		a.buckets[row.BucketIndex] = b
		a.total += row.SampleCount
	}
	result := make([]dtos.UploadTimingSummary, 0, len(accs))
	for key, a := range accs {
		if a.total == 0 {
			continue
		}
		result = append(result, dtos.UploadTimingSummary{
			Phase: key.phase, Kind: key.kind, Platform: key.platform, SampleCount: a.total,
			P50: histogramPercentile(a.buckets, a.total, 0.5),
			P95: histogramPercentile(a.buckets, a.total, 0.95),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Platform < b.Platform
	})
	return result
}

func loadUploadTimingSummary(eventID uuid.UUID, days int, now time.Time) ([]dtos.UploadTimingSummary, error) {
	if configuration.DB == nil {
		return []dtos.UploadTimingSummary{}, nil
	}
	if days < 1 {
		days = 7
	}
	from := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	var rows []models.EventUploadTimingDaily
	if err := configuration.DB.Where("event_id = ? AND bucket_date >= ?", eventID, from).Find(&rows).Error; err != nil {
		return nil, err
	}
	return summarizeUploadTimings(rows), nil
}
