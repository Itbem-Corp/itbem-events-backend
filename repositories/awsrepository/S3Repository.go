package awsrepository

import (
	"bytes"
	"context"
	"errors"
	"events-stocks/configuration"
	"events-stocks/models"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"io"
)

var s3Client *s3.Client

func Init(_ *models.Config) {
	s3Client = configuration.GetS3Client(nil)
}

func UploadToS3(ctx context.Context, content []byte, key, contentType, bucket string) (string, error) {
	uploader := manager.NewUploader(s3Client)

	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(contentType),
		ACL:         s3Types.ObjectCannedACLPublicRead,
	})
	if err != nil {
		return "", err
	}

	return GetS3URL(bucket, key), nil
}

func GetS3URL(bucket, key string) string {
	return fmt.Sprintf("https://%s.s3.amazonaws.com/%s", bucket, key)
}

func CheckS3ObjectExists(ctx context.Context, key, bucket string) (bool, error) {
	_, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func DeleteS3Object(ctx context.Context, key, bucket string) error {
	_, err := s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

func ListS3ObjectsWithPrefix(ctx context.Context, prefix, bucket string) ([]string, error) {
	var keys []string

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}

	return keys, nil
}

func GetS3Object(ctx context.Context, key, bucket string) (io.ReadCloser, error) {
	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
