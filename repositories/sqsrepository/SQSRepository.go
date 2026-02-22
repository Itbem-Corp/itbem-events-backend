// Package sqsrepository provides fire-and-forget publishing to AWS SQS.
// Used for async media processing (image optimization + video transcoding) via the
// itbem-media-processor Lambda project.
//
// Two separate queues allow independent concurrency and timeout settings:
//   - Image queue: low memory, fast Lambda (BatchSize=5)
//   - Video queue: high memory, 15min Lambda (BatchSize=1)
//
// If both queue URLs are empty the package is a no-op — the backend works
// normally without SQS configured.
package sqsrepository

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// MediaProcessMessage is the payload sent to the SQS queue for async media processing.
// The Lambda reads this, downloads the raw file, optimizes it, re-uploads, then
// calls back PUT /api/moments/:id/content to update the Moment record.
type MediaProcessMessage struct {
	MomentID    string `json:"moment_id"`
	EventID     string `json:"event_id"`
	RawS3Key    string `json:"raw_s3_key"`   // e.g. "moments/{eventID}/raw/{uuid}.jpg"
	Bucket      string `json:"bucket"`
	ContentType string `json:"content_type"` // e.g. "image/jpeg", "video/mp4"
	IsVideo     bool   `json:"is_video"`
}

// VideoTranscodeMessage is kept as an alias for backward compatibility.
// Deprecated: use MediaProcessMessage.
type VideoTranscodeMessage = MediaProcessMessage

var (
	sqsClient       *sqs.Client
	imageQueueURL   string
	videoQueueURL   string
	once            sync.Once
)

// Init initialises the SQS client with separate image and video queue URLs.
// Must be called once at server startup. Missing queue URLs disable that type of processing.
func Init(region, accessKeyID, secretAccessKey, imgQueue, vidQueue string) {
	once.Do(func() {
		imageQueueURL = imgQueue
		videoQueueURL = vidQueue

		if imgQueue == "" && vidQueue == "" {
			slog.Info("sqsrepository: no SQS queues configured — async media processing disabled")
			return
		}

		cfg, err := awsconfig.LoadDefaultConfig(
			context.Background(),
			awsconfig.WithRegion(region),
			awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
			),
		)
		if err != nil {
			slog.Error("sqsrepository: failed to load AWS config", "error", err)
			return
		}
		sqsClient = sqs.NewFromConfig(cfg)
		slog.Info("sqsrepository: SQS client initialised",
			"image_queue", imgQueue != "",
			"video_queue", vidQueue != "",
		)
	})
}

// PublishMediaJob routes the media processing job to the correct SQS queue
// (image queue or video queue) based on msg.IsVideo.
// Returns an error so callers can log failures and offer manual requeue.
// If the target queue is not configured, the call is a no-op (returns nil).
func PublishMediaJob(msg MediaProcessMessage) error {
	if sqsClient == nil {
		return nil
	}

	targetQueue := imageQueueURL
	if msg.IsVideo {
		targetQueue = videoQueueURL
	}
	if targetQueue == "" {
		slog.Debug("sqsrepository: queue not configured for type",
			"is_video", msg.IsVideo, "moment_id", msg.MomentID)
		return nil
	}

	body, err := json.Marshal(msg)
	if err != nil {
		slog.Error("sqsrepository: failed to marshal SQS message", "error", err)
		return fmt.Errorf("SQS marshal failed: %w", err)
	}
	_, err = sqsClient.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:    aws.String(targetQueue),
		MessageBody: aws.String(string(body)),
	})
	if err != nil {
		slog.Error("sqsrepository: failed to send SQS message",
			"moment_id", msg.MomentID, "is_video", msg.IsVideo, "error", err)
		return fmt.Errorf("SQS publish failed: %w", err)
	}
	return nil
}
