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
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/internal/products"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// MediaProcessMessage is kept as an alias for backward compatibility.
type MediaProcessMessage = dtos.MediaProcessMessage

// VideoTranscodeMessage is kept as an alias for backward compatibility.
// Deprecated: use MediaProcessMessage.
type VideoTranscodeMessage = MediaProcessMessage

var (
	sqsClient     *sqs.Client
	imageQueueURL string
	videoQueueURL string
	once          sync.Once
)

const mediaPublishTimeout = 5 * time.Second

func newMediaPublishContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), mediaPublishTimeout)
}

type Publisher struct{}

func NewPublisher() *Publisher { return &Publisher{} }

func (p *Publisher) PublishMediaJob(msg MediaProcessMessage) (bool, error) {
	return PublishMediaJob(msg)
}

// Init initialises the SQS client with separate image and video queue URLs.
// Must be called once at server startup. Missing queue URLs disable that type of processing.
func Init(region, accessKeyID, secretAccessKey, imgQueue, vidQueue string, endpoints ...string) {
	once.Do(func() {
		imageQueueURL = imgQueue
		videoQueueURL = vidQueue

		if imgQueue == "" && vidQueue == "" {
			slog.Info("sqsrepository: no SQS queues configured — async media processing disabled")
			return
		}

		cfg, err := configuration.LoadAWSConfig(context.Background(), region, accessKeyID, secretAccessKey)
		if err != nil {
			slog.Error("sqsrepository: failed to load AWS config", "error", err)
			return
		}
		endpoint := ""
		if len(endpoints) > 0 {
			endpoint = endpoints[0]
		}
		sqsClient = sqs.NewFromConfig(cfg, configuration.SQSClientOptions(endpoint))
		slog.Info("sqsrepository: SQS client initialised",
			"image_queue", imgQueue != "",
			"video_queue", vidQueue != "",
		)
	})
}

// PublishMediaJob routes the media processing job to the correct SQS queue
// (image queue or video queue) based on msg.IsVideo.
// Returns (enqueued, error): enqueued is true only when the message was actually
// sent to SQS. When SQS is not configured the call is a no-op (false, nil).
func PublishMediaJob(msg MediaProcessMessage) (bool, error) {
	if strings.TrimSpace(msg.Application) == "" {
		msg.Application = products.DefaultCode.String()
	} else if definition, known := products.Resolve(msg.Application); known {
		msg.Application = definition.Code.String()
	} else {
		return false, fmt.Errorf("unsupported media job product %q", msg.Application)
	}
	if strings.TrimSpace(msg.CorrelationID) == "" {
		msg.CorrelationID = strings.TrimSpace(msg.JobID)
	}
	if strings.TrimSpace(msg.SourceRevision) == "" {
		msg.SourceRevision = strings.TrimSpace(os.Getenv("SOURCE_REVISION"))
	}
	if sqsClient == nil {
		return false, nil
	}

	targetQueue := imageQueueURL
	if msg.IsVideo {
		targetQueue = videoQueueURL
	}
	if targetQueue == "" {
		slog.Debug("sqsrepository: queue not configured for type",
			"is_video", msg.IsVideo, "moment_id", msg.MomentID)
		return false, nil
	}

	body, err := json.Marshal(msg)
	if err != nil {
		slog.Error("sqsrepository: failed to marshal SQS message", "error", err)
		return false, fmt.Errorf("SQS marshal failed: %w", err)
	}
	ctx, cancel := newMediaPublishContext()
	defer cancel()
	_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(targetQueue),
		MessageBody: aws.String(string(body)),
	})
	if err != nil {
		slog.Error("sqsrepository: failed to send SQS message",
			"moment_id", msg.MomentID, "is_video", msg.IsVideo, "error", err)
		return false, fmt.Errorf("SQS publish failed: %w", err)
	}
	return true, nil
}
