package automationagent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const runtimeAuthProbeTimeout = 20 * time.Second

type queueMetadataClient interface {
	GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

type RuntimeAuthProbeReport struct {
	Ready             bool   `json:"ready"`
	Status            string `json:"status"`
	QueueAccess       string `json:"queue_access"`
	NetworkChecksMade bool   `json:"network_checks_made"`
}

// ProbeRuntimeAuth exercises the configured AWS credential chain and the
// exact lane queue without receiving or mutating work. It deliberately emits
// no account, role, queue URL or ARN. On an external Linux host this forces a
// broken Roles Anywhere certificate, TPM key, profile or lane IAM policy to
// fail before the long-lived worker can lease a task.
func ProbeRuntimeAuth(ctx context.Context, config RuntimeConfig, client queueMetadataClient) (RuntimeAuthProbeReport, error) {
	if client == nil {
		return RuntimeAuthProbeReport{}, errors.New("AWS runtime authentication probe is unavailable")
	}
	probeContext, cancel := context.WithTimeout(ctx, runtimeAuthProbeTimeout)
	defer cancel()
	output, err := client.GetQueueAttributes(probeContext, &sqs.GetQueueAttributesInput{
		QueueUrl:       &config.QueueURL,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return RuntimeAuthProbeReport{}, errors.New("AWS runtime authentication or scoped queue access failed")
	}
	if output == nil || strings.TrimSpace(output.Attributes[string(types.QueueAttributeNameQueueArn)]) == "" {
		return RuntimeAuthProbeReport{}, errors.New("AWS runtime queue identity could not be verified")
	}
	return RuntimeAuthProbeReport{Ready: true, Status: "authenticated", QueueAccess: "verified", NetworkChecksMade: true}, nil
}
