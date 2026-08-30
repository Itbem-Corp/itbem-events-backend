package automationagent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

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
		return nil, fmt.Errorf("read private automation input: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read private automation input: %w", err)
	}
	return data, nil
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
