package automationagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

// normalizeLocalQueueURL keeps LocalStack virtual-host queue URLs reachable
// through an explicit loopback endpoint without rewriting real AWS URLs.
func normalizeLocalQueueURL(queueURL, localEndpoint string) (string, error) {
	queue, err := url.Parse(strings.TrimSpace(queueURL))
	if err != nil || queue.Hostname() == "" || queue.User != nil || queue.RawQuery != "" || queue.Fragment != "" || queue.Path == "" || queue.Path == "/" {
		return "", fmt.Errorf("queue URL must be an absolute endpoint with a queue path and no credentials, query, or fragment")
	}
	localStackHost := strings.HasSuffix(strings.ToLower(queue.Hostname()), ".localhost.localstack.cloud")
	if queue.Scheme != "https" && !(queue.Scheme == "http" && (isLoopbackHost(queue.Hostname()) || (strings.TrimSpace(localEndpoint) != "" && localStackHost))) {
		return "", fmt.Errorf("queue URL must use HTTPS or loopback HTTP")
	}
	if strings.TrimSpace(localEndpoint) == "" {
		return queue.String(), nil
	}
	endpoint, err := url.Parse(strings.TrimSpace(localEndpoint))
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || !isLoopbackHost(endpoint.Hostname()) {
		return "", fmt.Errorf("local SQS endpoint must be loopback HTTP")
	}
	if !strings.HasSuffix(strings.ToLower(queue.Hostname()), ".localhost.localstack.cloud") {
		return queueURL, nil
	}
	queue.Scheme, queue.Host, queue.User = endpoint.Scheme, endpoint.Host, nil
	return queue.String(), nil
}

type AWSRuntime struct {
	SQS *sqs.Client
	S3  *s3.Client
}

func NewAWSRuntime(ctx context.Context, config RuntimeConfig) (AWSRuntime, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(config.AWSRegion))
	if err != nil {
		return AWSRuntime{}, fmt.Errorf("load AWS configuration: %w", err)
	}
	sqsClient := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) {
		if config.SQSEndpoint != "" {
			options.BaseEndpoint = aws.String(config.SQSEndpoint)
		}
	})
	s3Client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		if config.S3Endpoint != "" {
			options.BaseEndpoint = aws.String(config.S3Endpoint)
			options.UsePathStyle = true
		}
	})
	return AWSRuntime{SQS: sqsClient, S3: s3Client}, nil
}

type AWSObjectStore struct{ client *s3.Client }

func NewAWSObjectStore(client *s3.Client) *AWSObjectStore { return &AWSObjectStore{client: client} }

func (s *AWSObjectStore) Get(ctx context.Context, bucket, key string) ([]byte, error) {
	response, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		if automationObjectMissing(err) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("read private automation input: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read private automation input: %w", err)
	}
	return data, nil
}

func automationObjectMissing(err error) bool {
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(strings.TrimSpace(apiErr.ErrorCode())) {
		case "notfound", "nosuchkey", "nosuchobject":
			return true
		}
	}
	var statusErr interface{ HTTPStatusCode() int }
	return errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusNotFound
}

func (s *AWSObjectStore) PutEncryptedJSON(ctx context.Context, bucket, key string, body []byte) error {
	return s.PutEncryptedObject(ctx, bucket, key, body, "application/json")
}

func (s *AWSObjectStore) PutEncryptedObject(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentType: aws.String(contentType), ContentLength: aws.Int64(int64(len(body))), ServerSideEncryption: s3types.ServerSideEncryptionAes256})
	if err != nil {
		return fmt.Errorf("write encrypted private automation result: %w", err)
	}
	return nil
}
