package moments

import (
	"errors"
	"testing"

	"events-stocks/dtos"
	"events-stocks/models"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type casMomentRepo struct {
	*mockMomentRepo
	beginFunc func(id, eventID uuid.UUID, inputKey, jobID string) (int64, error)
	applyFunc func(id, eventID uuid.UUID, jobID string, generation int64, allowedCurrentStatuses []string, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64) (bool, error)
}

func (r *casMomentRepo) BeginMediaProcessingJob(id, eventID uuid.UUID, inputKey, jobID string) (int64, error) {
	return r.beginFunc(id, eventID, inputKey, jobID)
}

func (r *casMomentRepo) ApplyMediaProcessingUpdate(id, eventID uuid.UUID, jobID string, generation int64, allowedCurrentStatuses []string, contentURL, processingStatus, thumbnailURL, errorMessage string, durationMs, originalBytes, optimizedBytes int64, _ models.MediaVariants) (bool, error) {
	return r.applyFunc(id, eventID, jobID, generation, allowedCurrentStatuses, contentURL, processingStatus, thumbnailURL, errorMessage, durationMs, originalBytes, optimizedBytes)
}

func TestEnqueueMediaProcessingPublishesCASIdentity(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	inputKey := "moments/" + eventID.String() + "/raw/photo.jpg"
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{},
		beginFunc: func(id, gotEventID uuid.UUID, gotInputKey, jobID string) (int64, error) {
			assert.Equal(t, momentID, id)
			assert.Equal(t, eventID, gotEventID)
			assert.Equal(t, inputKey, gotInputKey)
			_, err := uuid.FromString(jobID)
			require.NoError(t, err)
			return 7, nil
		},
		applyFunc: func(uuid.UUID, uuid.UUID, string, int64, []string, string, string, string, string, int64, int64, int64) (bool, error) {
			t.Fatal("callback CAS should not run")
			return false, nil
		},
	}
	var published dtos.MediaProcessMessage
	publisher := &mockMediaJobPublisher{PublishFunc: func(msg dtos.MediaProcessMessage) (bool, error) {
		published = msg
		return true, nil
	}}
	svc := NewMomentService(repo, nil, publisher)
	moment := &models.Moment{ID: momentID, EventID: &eventID, ContentURL: inputKey, ProcessingStatus: "pending"}

	require.True(t, svc.EnqueueMediaProcessing(moment, inputKey, "bucket", "image/jpeg"))
	assert.NotEmpty(t, published.JobID)
	assert.Equal(t, int64(7), published.Generation)
	assert.Equal(t, published.JobID, moment.ProcessingJobID)
	assert.Equal(t, int64(7), moment.ProcessingGeneration)
	assert.Equal(t, inputKey, moment.ProcessingInputKey)
}

func TestEnqueueMediaProcessingPersistsQueueFailureAsTerminal(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	inputKey := "moments/" + eventID.String() + "/raw/photo.jpg"
	jobID := ""
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{},
		beginFunc: func(id, gotEventID uuid.UUID, gotInputKey, gotJobID string) (int64, error) {
			jobID = gotJobID
			return 4, nil
		},
		applyFunc: func(id, gotEventID uuid.UUID, gotJobID string, generation int64, allowed []string, contentURL, status, thumbnail, errorMessage string, durationMs, originalBytes, optimizedBytes int64) (bool, error) {
			assert.Equal(t, momentID, id)
			assert.Equal(t, eventID, gotEventID)
			assert.Equal(t, jobID, gotJobID)
			assert.Equal(t, int64(4), generation)
			assert.Equal(t, []string{"pending"}, allowed)
			assert.Equal(t, inputKey, contentURL)
			assert.Equal(t, "failed", status)
			assert.Equal(t, "media processing queue unavailable", errorMessage)
			return true, nil
		},
	}
	publisher := &mockMediaJobPublisher{PublishFunc: func(dtos.MediaProcessMessage) (bool, error) {
		return false, errors.New("SQS unavailable")
	}}
	svc := NewMomentService(repo, nil, publisher)
	moment := &models.Moment{ID: momentID, EventID: &eventID, ContentURL: inputKey, ProcessingStatus: "pending"}

	assert.False(t, svc.EnqueueMediaProcessing(moment, inputKey, "bucket", "image/jpeg"))
	assert.Equal(t, "failed", moment.ProcessingStatus)
	assert.Equal(t, "media processing queue unavailable", moment.ErrorMessage)
}

func TestRequeueMediaProcessingCompensatesPublishFailure(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	inputKey := "moments/" + eventID.String() + "/raw/video.mov"
	jobID := ""
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{},
		beginFunc: func(id, gotEventID uuid.UUID, gotInputKey, gotJobID string) (int64, error) {
			jobID = gotJobID
			return 8, nil
		},
		applyFunc: func(id, gotEventID uuid.UUID, gotJobID string, generation int64, allowed []string, contentURL, status, thumbnail, errorMessage string, durationMs, originalBytes, optimizedBytes int64) (bool, error) {
			assert.Equal(t, jobID, gotJobID)
			assert.Equal(t, int64(8), generation)
			assert.Equal(t, []string{"pending"}, allowed)
			assert.Equal(t, "failed", status)
			return true, nil
		},
	}
	publisher := &mockMediaJobPublisher{PublishFunc: func(dtos.MediaProcessMessage) (bool, error) {
		return false, errors.New("SQS unavailable")
	}}
	svc := NewMomentService(repo, nil, publisher)
	moment := &models.Moment{
		ID: momentID, EventID: &eventID, ContentURL: inputKey, ContentType: "video/quicktime",
		ProcessingStatus: "failed", ErrorMessage: "old failure",
	}

	err := svc.RequeueMoment(moment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requeue SQS publish failed")
	assert.Equal(t, "failed", moment.ProcessingStatus)
	assert.Equal(t, "media processing queue unavailable", moment.ErrorMessage)
}

func TestBatchReoptimizeRequiresEnqueueConfirmationAndRestoresWithCAS(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	optimizedKey := "moments/" + eventID.String() + "/photos/" + momentID.String() + ".webp"
	jobID := ""
	restored := false
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{GetMomentsByIDsFunc: func([]uuid.UUID) ([]models.Moment, error) {
			return []models.Moment{{
				ID: momentID, EventID: &eventID, ContentURL: optimizedKey,
				ContentType: "image/webp", ProcessingStatus: "done",
			}}, nil
		}},
		beginFunc: func(id, gotEventID uuid.UUID, inputKey, gotJobID string) (int64, error) {
			jobID = gotJobID
			return 12, nil
		},
		applyFunc: func(id, gotEventID uuid.UUID, gotJobID string, generation int64, allowed []string, contentURL, status, thumbnail, errorMessage string, durationMs, originalBytes, optimizedBytes int64) (bool, error) {
			restored = true
			assert.Equal(t, jobID, gotJobID)
			assert.Equal(t, int64(12), generation)
			assert.Equal(t, []string{"pending"}, allowed)
			assert.Equal(t, optimizedKey, contentURL)
			assert.Equal(t, "done", status)
			assert.Empty(t, errorMessage)
			return true, nil
		},
	}
	publisher := &mockMediaJobPublisher{PublishFunc: func(dtos.MediaProcessMessage) (bool, error) {
		return false, nil
	}}

	succeeded, skipped, failed, err := NewMomentService(repo, nil, publisher).BatchReoptimize([]uuid.UUID{momentID})
	require.NoError(t, err)
	assert.Zero(t, succeeded)
	assert.Zero(t, skipped)
	assert.Equal(t, 1, failed)
	assert.True(t, restored)
}

func TestApplyMediaProcessingCallbackUsesValidatedCAS(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	inputKey := "moments/" + eventID.String() + "/raw/video.mov"
	outputKey := "moments/" + eventID.String() + "/videos/" + momentID.String() + ".mp4"
	thumbnailKey := "moments/" + eventID.String() + "/videos/" + momentID.String() + "-thumb.webp"
	jobID := uuid.Must(uuid.NewV4()).String()
	moment := &models.Moment{
		ID:                   momentID,
		EventID:              &eventID,
		ContentURL:           inputKey,
		ContentType:          "video/quicktime",
		ProcessingStatus:     "processing",
		ProcessingGeneration: 3,
		ProcessingJobID:      jobID,
		ProcessingInputKey:   inputKey,
	}
	applied := false
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{GetMomentByIDFunc: func(uuid.UUID) (*models.Moment, error) { return moment, nil }},
		beginFunc:      func(uuid.UUID, uuid.UUID, string, string) (int64, error) { return 0, nil },
		applyFunc: func(id, gotEventID uuid.UUID, gotJobID string, generation int64, allowed []string, contentURL, status, thumbnail, errorMessage string, durationMs, originalBytes, optimizedBytes int64) (bool, error) {
			applied = true
			assert.Equal(t, momentID, id)
			assert.Equal(t, eventID, gotEventID)
			assert.Equal(t, jobID, gotJobID)
			assert.Equal(t, int64(3), generation)
			assert.Equal(t, []string{"pending", "processing"}, allowed)
			assert.Equal(t, outputKey, contentURL)
			assert.Equal(t, "done", status)
			assert.Equal(t, thumbnailKey, thumbnail)
			return true, nil
		},
	}
	svc := NewMomentService(repo, nil)

	err := svc.ApplyMediaProcessingCallback(dtos.MediaProcessingCallback{
		MomentID: momentID.String(), EventID: eventID.String(), JobID: jobID, Generation: 3,
		ObjectKey: outputKey, ThumbnailObjectKey: thumbnailKey, ProcessingStatus: "done",
		ProcessingDurationMs: 1200, OriginalSizeBytes: 1000, OptimizedSizeBytes: 500,
	})
	require.NoError(t, err)
	assert.True(t, applied)
}

func TestValidateMediaVariantsAcceptsOwnedResponsiveImages(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	moment := &models.Moment{
		ID: momentID, EventID: &eventID, ContentType: "image/jpeg",
		ProcessingInputKey: "moments/" + eventID.String() + "/raw/photo.jpg",
	}
	variants, err := validateMediaVariants(moment, "done", []dtos.MediaVariant{
		{ObjectKey: "moments/" + eventID.String() + "/photos/" + momentID.String() + "-480.webp", Width: 480, Format: "WEBP", Bytes: 1200},
		{ObjectKey: "moments/" + eventID.String() + "/photos/" + momentID.String() + "-960.webp", Width: 960, Format: "webp", Bytes: 3200},
	})

	require.NoError(t, err)
	require.Equal(t, models.MediaVariants{
		{ObjectKey: "moments/" + eventID.String() + "/photos/" + momentID.String() + "-480.webp", Width: 480, Format: "webp", Bytes: 1200},
		{ObjectKey: "moments/" + eventID.String() + "/photos/" + momentID.String() + "-960.webp", Width: 960, Format: "webp", Bytes: 3200},
	}, variants)
}

func TestValidateMediaVariantsRejectsUnsafeMetadata(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	moment := &models.Moment{ID: momentID, EventID: &eventID, ContentType: "image/jpeg"}
	validKey := "moments/" + eventID.String() + "/photos/" + momentID.String() + "-480.webp"

	tests := []struct {
		name     string
		variants []dtos.MediaVariant
	}{
		{name: "foreign key", variants: []dtos.MediaVariant{{ObjectKey: "moments/other/photos/other-480.webp", Width: 480, Format: "webp"}}},
		{name: "duplicate width", variants: []dtos.MediaVariant{{ObjectKey: validKey, Width: 480, Format: "webp"}, {ObjectKey: validKey, Width: 480, Format: "webp"}}},
		{name: "negative bytes", variants: []dtos.MediaVariant{{ObjectKey: validKey, Width: 480, Format: "webp", Bytes: -1}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateMediaVariants(moment, "done", test.variants)
			require.ErrorIs(t, err, ErrInvalidMomentProcessingCallback)
		})
	}
}

func TestApplyMediaProcessingCallbackRejectsStaleJobAndForeignKey(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	inputKey := "moments/" + eventID.String() + "/raw/photo.jpg"
	jobID := uuid.Must(uuid.NewV4()).String()
	moment := &models.Moment{
		ID: momentID, EventID: &eventID, ContentURL: inputKey, ContentType: "image/jpeg",
		ProcessingStatus: "processing", ProcessingGeneration: 4, ProcessingJobID: jobID, ProcessingInputKey: inputKey,
	}
	applyCalls := 0
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{GetMomentByIDFunc: func(uuid.UUID) (*models.Moment, error) { return moment, nil }},
		beginFunc:      func(uuid.UUID, uuid.UUID, string, string) (int64, error) { return 0, nil },
		applyFunc: func(uuid.UUID, uuid.UUID, string, int64, []string, string, string, string, string, int64, int64, int64) (bool, error) {
			applyCalls++
			return true, nil
		},
	}
	svc := NewMomentService(repo, nil)

	err := svc.ApplyMediaProcessingCallback(dtos.MediaProcessingCallback{
		MomentID: momentID.String(), EventID: eventID.String(), JobID: uuid.Must(uuid.NewV4()).String(), Generation: 3,
		ObjectKey: "moments/" + eventID.String() + "/photos/" + momentID.String() + ".webp", ProcessingStatus: "done",
	})
	require.ErrorIs(t, err, ErrStaleMomentProcessingCallback)

	err = svc.ApplyMediaProcessingCallback(dtos.MediaProcessingCallback{
		MomentID: momentID.String(), EventID: eventID.String(), JobID: jobID, Generation: 4,
		ObjectKey: "moments/" + eventID.String() + "/photos/another-moment.webp", ProcessingStatus: "done",
	})
	require.ErrorIs(t, err, ErrInvalidMomentProcessingCallback)
	assert.Zero(t, applyCalls)
}

func TestApplyMediaProcessingCallbackTerminalReplayIsIdempotent(t *testing.T) {
	momentID := uuid.Must(uuid.NewV4())
	eventID := uuid.Must(uuid.NewV4())
	jobID := uuid.Must(uuid.NewV4()).String()
	outputKey := "moments/" + eventID.String() + "/photos/" + momentID.String() + ".webp"
	moment := &models.Moment{
		ID: momentID, EventID: &eventID, ContentURL: outputKey, ContentType: "image/jpeg",
		ProcessingStatus: "done", ProcessingGeneration: 1, ProcessingJobID: jobID,
		ProcessingInputKey: "moments/" + eventID.String() + "/raw/photo.jpg",
	}
	repo := &casMomentRepo{
		mockMomentRepo: &mockMomentRepo{GetMomentByIDFunc: func(uuid.UUID) (*models.Moment, error) { return moment, nil }},
		beginFunc:      func(uuid.UUID, uuid.UUID, string, string) (int64, error) { return 0, nil },
		applyFunc: func(uuid.UUID, uuid.UUID, string, int64, []string, string, string, string, string, int64, int64, int64) (bool, error) {
			t.Fatal("idempotent terminal replay must not write")
			return false, nil
		},
	}
	svc := NewMomentService(repo, nil)

	require.NoError(t, svc.ApplyMediaProcessingCallback(dtos.MediaProcessingCallback{
		MomentID: momentID.String(), EventID: eventID.String(), JobID: jobID, Generation: 1,
		ObjectKey: outputKey, ProcessingStatus: "done",
	}))
}
