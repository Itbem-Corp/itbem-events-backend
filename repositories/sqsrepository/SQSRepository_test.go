package sqsrepository

import (
	"testing"
	"time"
)

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

	enqueued, err := PublishMediaJob(MediaProcessMessage{})
	if err != nil {
		t.Fatalf("PublishMediaJob() error = %v", err)
	}
	if enqueued {
		t.Fatal("PublishMediaJob() enqueued without an initialized client")
	}
}
