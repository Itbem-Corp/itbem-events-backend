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
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

var s3Client *s3.Client

func Init(_ *models.Config) {
	s3Client = configuration.GetS3Client(nil)
}

func UploadToS3(ctx context.Context, content []byte, key, contentType, bucket string) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	uploader := manager.NewUploader(s3Client)

	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}

	return GetS3URL(bucket, key), nil
}

func GetS3URL(bucket, key string) string {
	// CDN_BASE_URL is read on every call intentionally: allows CDN to be enabled
	// via env update (ECS redeploy) without a binary rebuild. Per-call cost is sub-microsecond.
	if base := os.Getenv("CDN_BASE_URL"); base != "" {
		return strings.TrimRight(base, "/") + "/" + key
	}
	return fmt.Sprintf("https://%s.s3.amazonaws.com/%s", bucket, key)
}

// RewriteToCDN rewrites a stored S3 URL to the CDN base URL.
// Returns the original URL unchanged if CDN_BASE_URL is not set or URL is empty.
// Handles both virtual-hosted (bucket.s3.amazonaws.com/key) and
// regional (bucket.s3.region.amazonaws.com/key) S3 URL formats.
func RewriteToCDN(rawURL string) string {
	// CDN_BASE_URL is read on every call intentionally: allows CDN to be enabled
	// via env update (ECS redeploy) without a binary rebuild. Per-call cost is sub-microsecond.
	base := os.Getenv("CDN_BASE_URL")
	if base == "" || rawURL == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	if !strings.Contains(host, ".s3.") && !strings.HasSuffix(host, ".amazonaws.com") {
		return rawURL // already a CDN URL or unknown format
	}

	path := strings.TrimPrefix(u.Path, "/")

	// Path-style URL (s3.amazonaws.com/bucket/key): strip the bucket-name prefix.
	if !strings.Contains(host, ".s3.") {
		if parts := strings.SplitN(path, "/", 2); len(parts) == 2 {
			path = parts[1]
		}
	}

	return strings.TrimRight(base, "/") + "/" + path
}

// S3KeyFromURL extracts the bare S3 key from a URL.
// If the URL starts with CDN_BASE_URL it strips that prefix.
// If it starts with an S3 virtual-hosted or path-style prefix it strips that too.
// If the input is already a bare key (no scheme) it is returned unchanged.
func S3KeyFromURL(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	// Bare key — no scheme, nothing to strip.
	if !strings.Contains(rawURL, "://") {
		return rawURL
	}
	// CDN URL: https://cdn.example.com/moments/... → moments/...
	if base := os.Getenv("CDN_BASE_URL"); base != "" {
		prefix := strings.TrimRight(base, "/") + "/"
		if strings.HasPrefix(rawURL, prefix) {
			return strings.TrimPrefix(rawURL, prefix)
		}
	}
	// S3 URL: https://bucket.s3.amazonaws.com/key or https://s3.region.amazonaws.com/bucket/key
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := u.Hostname()
	path := strings.TrimPrefix(u.Path, "/")
	if strings.Contains(host, ".s3.") || strings.HasSuffix(host, ".amazonaws.com") {
		if !strings.Contains(host, ".s3.") {
			// path-style: strip bucket prefix
			if parts := strings.SplitN(path, "/", 2); len(parts) == 2 {
				return parts[1]
			}
		}
		return path
	}
	return rawURL
}

func CheckS3ObjectExists(ctx context.Context, key, bucket string) (bool, error) {
	s3Client := configuration.GetS3Client(nil)
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
	s3Client := configuration.GetS3Client(nil)
	_, err := s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

// GetS3ObjectSize returns the byte size of an S3 object via HeadObject.
// Returns an error (including NoSuchKey) if the object does not exist.
func GetS3ObjectSize(ctx context.Context, key, bucket string) (int64, error) {
	size, _, err := GetS3ObjectMeta(ctx, key, bucket)
	return size, err
}

// GetS3ObjectMeta returns the byte size and Content-Type of an S3 object in a
// single HeadObject call. Use this when you need both values to avoid a double
// round-trip. Returns an error if the object does not exist.
func GetS3ObjectMeta(ctx context.Context, key, bucket string) (size int64, contentType string, err error) {
	s3Client := configuration.GetS3Client(nil)
	resp, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, "", err
	}
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}
	if resp.ContentType != nil {
		contentType = *resp.ContentType
	}
	return size, contentType, nil
}

// CopyS3Object performs a server-side copy within the same bucket.
// srcKey and dstKey are plain object keys (no bucket prefix).
func CopyS3Object(ctx context.Context, srcKey, dstKey, bucket string) error {
	s3Client := configuration.GetS3Client(nil)
	copySource := fmt.Sprintf("%s/%s", bucket, srcKey)
	_, err := s3Client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(dstKey),
	})
	return err
}

func ListS3ObjectsWithPrefix(ctx context.Context, prefix, bucket string) ([]string, error) {
	var keys []string
	s3Client := configuration.GetS3Client(nil)

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
	s3Client := configuration.GetS3Client(nil)
	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func GeneratePresignedURL(ctx context.Context, key, bucket string, expiresInMinutes int) (string, error) {
	s3Client := configuration.GetS3Client(nil)

	// Crea el input
	input := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}

	presignClient := s3.NewPresignClient(s3Client)

	resp, err := presignClient.PresignGetObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(expiresInMinutes) * time.Minute
	})
	if err != nil {
		return "", err
	}

	return resp.URL, nil
}

// CompletedPart holds the PartNumber and ETag returned by S3 for a single uploaded part.
// Used when assembling the final object via CompleteMultipartUpload.
type CompletedPart struct {
	PartNumber int
	ETag       string
}

// CreateMultipartUpload initiates a multipart upload and returns the upload ID.
// The caller must eventually call CompleteMultipartUpload or AbortMultipartUpload.
func CreateMultipartUpload(ctx context.Context, key, bucket, contentType string) (string, error) {
	client := configuration.GetS3Client(nil)
	resp, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(resp.UploadId), nil
}

// GetPresignedPartURL returns a short-lived presigned URL for uploading one part.
// partNumber is 1-based (S3 requirement). ttlMin is the URL lifetime in minutes.
func GetPresignedPartURL(ctx context.Context, key, bucket, uploadID string, partNumber, ttlMin int) (string, error) {
	client := configuration.GetS3Client(nil)
	presignClient := s3.NewPresignClient(client)
	pn := int32(partNumber)
	resp, err := presignClient.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: &pn,
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(ttlMin) * time.Minute
	})
	if err != nil {
		return "", err
	}
	return resp.URL, nil
}

// CompleteMultipartUpload assembles the uploaded parts into the final S3 object.
// parts must include every part number with its ETag (including surrounding quotes).
func CompleteMultipartUpload(ctx context.Context, key, bucket, uploadID string, parts []CompletedPart) error {
	client := configuration.GetS3Client(nil)
	completed := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		pn := int32(p.PartNumber)
		etag := p.ETag
		completed[i] = types.CompletedPart{PartNumber: &pn, ETag: &etag}
	}
	_, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	return err
}

// AbortMultipartUpload cancels a multipart upload and removes all uploaded parts.
// Call this on error or user cancellation to avoid orphaned storage charges.
func AbortMultipartUpload(ctx context.Context, key, bucket, uploadID string) error {
	client := configuration.GetS3Client(nil)
	_, err := client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	return err
}

// GeneratePresignedPutURL returns a short-lived presigned URL the browser can use
// to PUT a file directly to S3, bypassing the backend for the file bytes.
func GeneratePresignedPutURL(ctx context.Context, key, bucket, contentType string, expiresInMinutes int) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	presignClient := s3.NewPresignClient(s3Client)

	resp, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(expiresInMinutes) * time.Minute
	})
	if err != nil {
		return "", err
	}

	return resp.URL, nil
}
