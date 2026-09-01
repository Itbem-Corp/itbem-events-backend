package automationagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type runtimeProbeQueue struct {
	output *sqs.GetQueueAttributesOutput
	err    error
	input  *sqs.GetQueueAttributesInput
}

func (queue *runtimeProbeQueue) GetQueueAttributes(_ context.Context, input *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	queue.input = input
	return queue.output, queue.err
}

func TestProbeRuntimeAuthIsReadOnlyAndRedactsQueueIdentity(t *testing.T) {
	queueURL := "https://sqs.us-east-2.amazonaws.com/123456789012/private-engineering"
	queueARN := "arn:aws:sqs:us-east-2:123456789012:private-engineering"
	queue := &runtimeProbeQueue{output: &sqs.GetQueueAttributesOutput{Attributes: map[string]string{string(types.QueueAttributeNameQueueArn): queueARN}}}
	report, err := ProbeRuntimeAuth(context.Background(), RuntimeConfig{QueueURL: queueURL}, queue)
	if err != nil || !report.Ready || report.Status != "authenticated" || report.QueueAccess != "verified" || !report.NetworkChecksMade {
		t.Fatalf("runtime auth probe = %#v, %v", report, err)
	}
	if queue.input == nil || queue.input.QueueUrl == nil || *queue.input.QueueUrl != queueURL {
		t.Fatalf("probe did not inspect the configured lane queue: %#v", queue.input)
	}
	if len(queue.input.AttributeNames) != 1 || queue.input.AttributeNames[0] != types.QueueAttributeNameQueueArn {
		t.Fatalf("probe requested mutable or excessive queue data: %#v", queue.input.AttributeNames)
	}
}

func TestProbeRuntimeAuthFailsClosedWithoutLeakingAWSIdentity(t *testing.T) {
	queueURL := "https://sqs.us-east-2.amazonaws.com/123456789012/private-review"
	queue := &runtimeProbeQueue{err: errors.New("credential_process failed for arn:aws:iam::123456789012:role/private-review")}
	_, err := ProbeRuntimeAuth(context.Background(), RuntimeConfig{QueueURL: queueURL}, queue)
	if err == nil {
		t.Fatal("failed credential process was accepted")
	}
	if strings.Contains(err.Error(), queueURL) || strings.Contains(err.Error(), "123456789012") || strings.Contains(err.Error(), "private-review") {
		t.Fatalf("runtime auth probe leaked AWS identity: %v", err)
	}

	queue = &runtimeProbeQueue{output: &sqs.GetQueueAttributesOutput{Attributes: map[string]string{}}}
	if _, err := ProbeRuntimeAuth(context.Background(), RuntimeConfig{QueueURL: queueURL}, queue); err == nil {
		t.Fatal("queue response without an immutable ARN was accepted")
	}
}
