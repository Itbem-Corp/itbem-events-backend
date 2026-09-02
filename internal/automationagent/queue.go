package automationagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type QueueMessage struct {
	Body          string
	ReceiptHandle string
}

// QueuePriority is intentionally derived only from the allow-listed operation
// inside a decoded task message. It gives the single worker queue predictable
// service under load without trusting caller-supplied priority values.
type QueuePriority int

const (
	queuePriorityNormal QueuePriority = iota
	queuePriorityReview
)

func queueMessagePriority(raw QueueMessage) QueuePriority {
	message, err := DecodeTaskMessage(raw.Body)
	if err != nil {
		return queuePriorityNormal
	}
	if message.Payload.Operation == "code.review" {
		return queuePriorityReview
	}
	return queuePriorityNormal
}

const (
	queueVisibilityTimeoutSeconds  int32 = 900
	queueVisibilityHeartbeatPeriod       = 4 * time.Minute
	queueDeferredReviewSeconds     int32 = 30
	// A bounded review burst preserves fast PR feedback while guaranteeing a
	// queued QA, plan or implementation job eventually receives a slot.
	queueReviewBurstLimit = 3
)

func retryVisibilitySeconds(retryable *RetryableError) int32 {
	delay := providerRetryDefaultDelay
	if retryable != nil && retryable.RetryAfter > 0 {
		delay = retryable.RetryAfter
	}
	if delay < providerRetryMinDelay {
		delay = providerRetryMinDelay
	}
	if delay > providerRetryMaxDelay {
		delay = providerRetryMaxDelay
	}
	return int32((delay + time.Second - 1) / time.Second)
}

type Queue interface {
	Receive(context.Context, int) ([]QueueMessage, error)
	Delete(context.Context, QueueMessage) error
}

type scheduledQueueMessage struct {
	raw       QueueMessage
	review    bool
	stopLease func()
}

// VisibilityExtendingQueue is deliberately optional so deterministic unit
// queues and non-SQS transports keep the same terminal-message contract. The
// AWS adapter implements it to retain a lease for long-running provider and
// browser QA operations.
type VisibilityExtendingQueue interface {
	ExtendVisibility(context.Context, QueueMessage, int32) error
}

// DeferrableQueue is optional for deterministic test transports. The SQS
// adapter uses it when the serial review lane already owns a waiting review:
// a newly received review is returned to the single shared queue with a short
// delay instead of consuming a second local lease or idling normal workers.
type DeferrableQueue interface {
	Defer(context.Context, QueueMessage, int32) error
}

type AWSQueue struct {
	client   *sqs.Client
	queueURL string
}

func NewAWSQueue(client *sqs.Client, queueURL string) (*AWSQueue, error) {
	if client == nil || queueURL == "" {
		return nil, fmt.Errorf("SQS client and queue URL are required")
	}
	return &AWSQueue{client: client, queueURL: queueURL}, nil
}

func (q *AWSQueue) Receive(ctx context.Context, limit int) ([]QueueMessage, error) {
	if limit < 1 {
		return nil, nil
	}
	if limit > 10 {
		limit = 10
	}
	response, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(q.queueURL), MaxNumberOfMessages: int32(limit), WaitTimeSeconds: 20, VisibilityTimeout: queueVisibilityTimeoutSeconds})
	if err != nil {
		return nil, fmt.Errorf("receive automation message: %w", err)
	}
	messages := make([]QueueMessage, 0, len(response.Messages))
	for _, message := range response.Messages {
		if message.Body != nil && message.ReceiptHandle != nil {
			messages = append(messages, QueueMessage{Body: *message.Body, ReceiptHandle: *message.ReceiptHandle})
		}
	}
	return messages, nil
}

func (q *AWSQueue) ExtendVisibility(ctx context.Context, message QueueMessage, timeoutSeconds int32) error {
	if strings.TrimSpace(message.ReceiptHandle) == "" || timeoutSeconds < 1 || timeoutSeconds > 43_200 {
		return fmt.Errorf("automation message visibility request is invalid")
	}
	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(q.queueURL), ReceiptHandle: aws.String(message.ReceiptHandle), VisibilityTimeout: timeoutSeconds})
	if err != nil {
		return fmt.Errorf("extend automation message visibility: %w", err)
	}
	return nil
}

func (q *AWSQueue) Defer(ctx context.Context, message QueueMessage, delaySeconds int32) error {
	if strings.TrimSpace(message.ReceiptHandle) == "" || delaySeconds < 1 || delaySeconds > 43_200 {
		return fmt.Errorf("automation message defer request is invalid")
	}
	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(q.queueURL), ReceiptHandle: aws.String(message.ReceiptHandle), VisibilityTimeout: delaySeconds})
	if err != nil {
		return fmt.Errorf("defer automation message: %w", err)
	}
	return nil
}

func (q *AWSQueue) Delete(ctx context.Context, message QueueMessage) error {
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(q.queueURL), ReceiptHandle: aws.String(message.ReceiptHandle)})
	if err != nil {
		return fmt.Errorf("delete automation message: %w", err)
	}
	return nil
}

// ProcessQueueMessage deletes only terminally processed messages. Invalid,
// callback and retryable failures remain visible to SQS and eventually DLQ.
func ProcessQueueMessage(ctx context.Context, worker *Worker, queue Queue, raw QueueMessage) error {
	message, err := DecodeTaskMessage(raw.Body)
	if err != nil {
		return err
	}
	// HTTP gateway receipt handles are sealed task leases. Carrying the lease in
	// context binds every object read/write to this exact delivery without
	// changing the transport-neutral worker and store interfaces.
	if _, ok := queue.(*HTTPGateway); ok {
		ctx = context.WithValue(ctx, gatewayLeaseContextKey{}, raw.ReceiptHandle)
	}
	if err := worker.Process(ctx, message); err != nil {
		return err
	}
	return queue.Delete(ctx, raw)
}

func RunQueue(ctx context.Context, worker *Worker, queue Queue, concurrency int, logger *slog.Logger) error {
	if concurrency < 1 || concurrency > maxAgentConcurrency {
		return fmt.Errorf("worker concurrency must be between 1 and %d", maxAgentConcurrency)
	}
	if logger == nil {
		logger = slog.Default()
	}
	jobs := make(chan scheduledQueueMessage, concurrency)
	slots := make(chan struct{}, concurrency)
	// Reviews are intentionally serialized even when implementation/QA work
	// uses several worker slots. This keeps one coherent reviewer timeline per
	// local agent, avoids competing provider judgements for a burst of PRs, and
	// still leaves normal capacity available for independent non-review tasks.
	reviewSlot := make(chan struct{}, 1)
	var workers sync.WaitGroup
	for index := 0; index < concurrency; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for scheduled := range jobs {
				func() {
					defer func() { <-slots }()
					if scheduled.review {
						defer func() { <-reviewSlot }()
					}
					err := processQueueMessageWithVisibilityHeartbeat(ctx, worker, queue, scheduled.raw, logger, queueVisibilityHeartbeatPeriod)
					if err == nil {
						return
					}
					var retryable *RetryableError
					if errors.As(err, &retryable) {
						if extender, ok := queue.(VisibilityExtendingQueue); ok {
							retryContext, retryCancel := context.WithTimeout(ctx, 15*time.Second)
							if visibilityErr := extender.ExtendVisibility(retryContext, scheduled.raw, retryVisibilitySeconds(retryable)); visibilityErr != nil && retryContext.Err() == nil {
								logger.Warn("automation retry delay could not be applied; SQS default visibility remains active", "error", visibilityErr)
							}
							retryCancel()
						}
						logger.Warn("automation delivery retained for retry", "reason", retryable.Message, "retry_in_seconds", retryVisibilitySeconds(retryable))
						return
					}
					logger.Error("automation delivery retained", "error", err)
				}()
			}
		}()
	}
	defer func() {
		close(jobs)
		workers.Wait()
	}()
	pendingReviews := make([]scheduledQueueMessage, 0, 1)
	pendingWork := make([]QueueMessage, 0, concurrency)
	defer func() {
		for _, scheduled := range pendingReviews {
			if scheduled.stopLease != nil {
				scheduled.stopLease()
			}
		}
	}()
	reviewsSinceWork := 0
	nextPending := func() (scheduledQueueMessage, bool) {
		if len(pendingWork) > 0 && (len(pendingReviews) == 0 || len(reviewSlot) > 0 || reviewsSinceWork >= queueReviewBurstLimit) {
			message := pendingWork[0]
			pendingWork = pendingWork[1:]
			reviewsSinceWork = 0
			return scheduledQueueMessage{raw: message}, true
		}
		if len(pendingReviews) > 0 && len(reviewSlot) == 0 {
			message := pendingReviews[0]
			pendingReviews = pendingReviews[1:]
			reviewsSinceWork++
			return message, true
		}
		if len(pendingWork) > 0 {
			message := pendingWork[0]
			pendingWork = pendingWork[1:]
			reviewsSinceWork = 0
			return scheduledQueueMessage{raw: message}, true
		}
		return scheduledQueueMessage{}, false
	}
	dispatch := func(scheduled scheduledQueueMessage) error {
		if scheduled.stopLease != nil {
			scheduled.stopLease()
		}
		review := scheduled.review || queueMessagePriority(scheduled.raw) == queuePriorityReview
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		if review {
			select {
			case reviewSlot <- struct{}{}:
			case <-ctx.Done():
				<-slots
				return ctx.Err()
			}
		}
		select {
		case jobs <- scheduledQueueMessage{raw: scheduled.raw, review: review}:
			return nil
		case <-ctx.Done():
			if review {
				<-reviewSlot
			}
			<-slots
			return ctx.Err()
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		capacity := cap(slots) - len(slots)
		if capacity < 1 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		for capacity > 0 {
			scheduled, ok := nextPending()
			if !ok {
				break
			}
			if err := dispatch(scheduled); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			capacity--
		}
		if capacity < 1 {
			continue
		}
		// If a review is already retained locally behind the serialized lane,
		// stop receiving until that lane opens. AWS normally defers additional
		// reviews back to SQS, but this fail-closed branch also covers a failed
		// defer request or a test/alternate transport without that capability.
		// Prefetching another review here would create an unbounded collection of
		// invisible leases and eventually invite duplicate PR processing.
		if len(pendingReviews) > 0 && len(reviewSlot) > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		// Receive exactly one message per scheduling turn. SQS begins the
		// visibility lease at receive time, not when a local worker eventually
		// starts it. Prefetching a batch of reviews behind the serial review lane
		// can therefore make those leases expire and duplicate the same PR work.
		// The loop immediately receives again while capacity remains, preserving
		// concurrency for independent work without holding a local backlog.
		messages, err := queue.Receive(ctx, 1)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, raw := range messages {
			if queueMessagePriority(raw) == queuePriorityReview {
				if len(pendingReviews) > 0 || len(reviewSlot) > 0 {
					if deferrable, ok := queue.(DeferrableQueue); ok {
						deferCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
						err := deferrable.Defer(deferCtx, raw, queueDeferredReviewSeconds)
						cancel()
						if err == nil {
							continue
						}
						if ctx.Err() == nil {
							logger.Warn("queued automation review could not be deferred; its current lease remains active", "error", err)
						}
					}
					// Non-SQS deterministic transports do not support an explicit
					// defer. Retain their delivery so it is never lost; the normal
					// visibility heartbeat protects it until the serial lane opens.
				}
				// At most one review can wait locally. Its visibility lease is
				// renewed until it becomes the active serial review, preventing
				// SQS redelivery during a long preceding review.
				pendingReviews = append(pendingReviews, scheduledQueueMessage{raw: raw, review: true, stopLease: retainPendingQueueLease(ctx, queue, raw, logger)})
			} else {
				pendingWork = append(pendingWork, raw)
			}
		}
	}
}

func retainPendingQueueLease(ctx context.Context, queue Queue, raw QueueMessage, logger *slog.Logger) func() {
	extender, ok := queue.(VisibilityExtendingQueue)
	if !ok {
		return func() {}
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(queueVisibilityHeartbeatPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				heartbeatCtx, heartbeatCancel := context.WithTimeout(leaseCtx, 15*time.Second)
				err := extender.ExtendVisibility(heartbeatCtx, raw, queueVisibilityTimeoutSeconds)
				heartbeatCancel()
				if err != nil && leaseCtx.Err() == nil && logger != nil {
					logger.Warn("pending automation review visibility heartbeat failed; SQS may redeliver", "error", err)
				}
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func processQueueMessageWithVisibilityHeartbeat(ctx context.Context, worker *Worker, queue Queue, raw QueueMessage, logger *slog.Logger, period time.Duration) error {
	extender, ok := queue.(VisibilityExtendingQueue)
	if !ok {
		return ProcessQueueMessage(ctx, worker, queue, raw)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if period <= 0 {
		period = queueVisibilityHeartbeatPeriod
	}
	leaseContext, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(period)
		defer ticker.Stop()
		for {
			select {
			case <-leaseContext.Done():
				return
			case <-ticker.C:
				heartbeatContext, heartbeatCancel := context.WithTimeout(leaseContext, 15*time.Second)
				err := extender.ExtendVisibility(heartbeatContext, raw, queueVisibilityTimeoutSeconds)
				heartbeatCancel()
				if err != nil && leaseContext.Err() == nil {
					logger.Warn("automation visibility heartbeat failed; SQS may redeliver after the current lease", "error", err)
				}
			}
		}
	}()
	err := ProcessQueueMessage(leaseContext, worker, queue, raw)
	cancel()
	<-done
	return err
}
