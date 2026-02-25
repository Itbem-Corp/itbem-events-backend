package sqsrepository

import (
	"testing"
)

// TestPublishMediaJob_NoClient_ReturnsNoopWithNoError verifies that when
// sqsClient is nil (SQS not configured), PublishMediaJob is a no-op.
func TestPublishMediaJob_NoClient_ReturnsNoopWithNoError(t *testing.T) {
	// Reset singleton so sqsClient is nil
	sqsClient = nil
	imageQueueURL = "https://sqs.us-east-1.amazonaws.com/000/img"
	videoQueueURL = ""

	enqueued, err := PublishMediaJob(MediaProcessMessage{
		MomentID:    "moment-1",
		EventID:     "event-1",
		RawS3Key:    "moments/event-1/raw/file.jpg",
		Bucket:      "my-bucket",
		ContentType: "image/jpeg",
		IsVideo:     false,
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if enqueued {
		t.Fatal("expected enqueued=false when sqsClient is nil")
	}
}

// TestPublishMediaJob_ImageWithNoImageQueue_ReturnsNoop verifies that when
// the image queue is not configured, an image job is a no-op.
func TestPublishMediaJob_ImageWithNoImageQueue_ReturnsNoop(t *testing.T) {
	sqsClient = nil
	imageQueueURL = "" // image queue not configured
	videoQueueURL = "https://sqs.us-east-1.amazonaws.com/000/vid"

	enqueued, err := PublishMediaJob(MediaProcessMessage{IsVideo: false})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if enqueued {
		t.Fatal("expected enqueued=false when image queue empty")
	}
}
