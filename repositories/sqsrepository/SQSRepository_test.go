package sqsrepository

import (
	"errors"
	"testing"
	"time"
)

func validMediaMessage() MediaProcessMessage {
	return MediaProcessMessage{
		JobID:       "media-job-1",
		MomentID:    "moment-1",
		EventID:     "event-1",
		ObjectKey:   "events/event-1/raw/photo.jpg",
		RawS3Key:    "events/event-1/raw/photo.jpg",
		Bucket:      "private-media",
		ContentType: "image/jpeg",
	}
}

func TestMediaPublishTimeoutIsBounded(t *testing.T) {
	ctx, cancel := newMediaPublishContext()
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("media publish context must have a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 4*time.Second || remaining > mediaPublishTimeout {
		t.Fatalf("media publish deadline remaining = %s, want approximately %s", remaining, mediaPublishTimeout)
	}
}

func TestPublishMediaJobWithoutConfiguredClientIsExplicitNoop(t *testing.T) {
	previous := sqsClient
	sqsClient = nil
	t.Cleanup(func() { sqsClient = previous })

	enqueued, err := PublishMediaJob(validMediaMessage())
	if err != nil {
		t.Fatalf("PublishMediaJob() error = %v", err)
	}
	if enqueued {
		t.Fatal("PublishMediaJob() enqueued without an initialized client")
	}
}

func TestPublishSerializedRejectsUnavailableQueueForDurableRetry(t *testing.T) {
	previous := sqsClient
	sqsClient = nil
	t.Cleanup(func() { sqsClient = previous })

	body := `{"job_id":"media-job-1","moment_id":"moment-1","event_id":"event-1","object_key":"events/event-1/raw/photo.jpg","raw_s3_key":"events/event-1/raw/photo.jpg","bucket":"private-media","content_type":"image/jpeg"}`
	if err := PublishSerialized(body); !errors.Is(err, ErrQueueUnavailable) {
		t.Fatalf("PublishSerialized() error = %v, want ErrQueueUnavailable", err)
	}
}

func TestNormalizeMediaJobRequiresDurableIdentity(t *testing.T) {
	message := validMediaMessage()
	message.JobID = ""
	if _, err := NormalizeMediaJob(message); err == nil {
		t.Fatal("media job without an id was accepted")
	}
}
