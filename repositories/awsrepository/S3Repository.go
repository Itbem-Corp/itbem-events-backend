package awsrepository

import (
	"bytes"
	"context"
	"errors"
	"events-stocks/configuration"
	"events-stocks/dtos"
	"events-stocks/services/ports"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

const (
	singlePutThreshold = 16 * 1024 * 1024
	multipartPartSize  = 16 * 1024 * 1024
	metadataTimeout    = 30 * time.Second
	uploadTimeout      = 2 * time.Minute
	unconfirmedTagging = "upload-state=unconfirmed"
)

type StorageError struct {
	Operation string
	Kind      string
	Region    string
	Err       error
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

func (e *StorageError) Error() string {
	if e == nil {
		return "object storage error"
	}
	return fmt.Sprintf("object storage %s failed: %v", e.Operation, e.Err)
}

func (e *StorageError) Unwrap() error { return e.Err }

func (e *StorageError) ClientMessage() string {
	if e == nil {
		return "Media storage is temporarily unavailable. Please retry."
	}
	switch e.Kind {
	case "region":
		return "Media storage is using the wrong bucket region. The server configuration must be corrected."
	case "configuration":
		return "Media storage is not configured correctly."
	case "timeout":
		return "The media upload timed out. Please retry."
	default:
		return "Media storage is temporarily unavailable. Please retry."
	}
}

func (e *StorageError) Temporary() bool {
	return e != nil && (e.Kind == "timeout" || e.Kind == "unavailable")
}

func wrapStorageError(operation string, err error) error {
	if err == nil {
		return nil
	}
	kind := "unavailable"
	region := ""
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		kind = "timeout"
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PermanentRedirect", "AuthorizationHeaderMalformed", "IllegalLocationConstraintException":
			kind = "region"
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "NoSuchBucket":
			kind = "configuration"
		case "RequestTimeout", "SlowDown", "ServiceUnavailable", "InternalError":
			kind = "unavailable"
		}
		if value, ok := apiErr.(interface{ ErrorFault() smithy.ErrorFault }); ok && value.ErrorFault() == smithy.FaultClient && kind == "unavailable" {
			kind = "configuration"
		}
	}
	var regionProvider interface{ Region() string }
	if errors.As(err, &regionProvider) {
		region = regionProvider.Region()
	}
	return &StorageError{Operation: operation, Kind: kind, Region: region, Err: err}
}

func withStorageTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func UploadToS3(ctx context.Context, content []byte, key, contentType, bucket string) (string, error) {
	return UploadStreamToS3(ctx, bytes.NewReader(content), int64(len(content)), key, contentType, bucket)
}

// UploadEncryptedJSON writes small control-plane inputs with explicit S3
// server-side encryption. Automation input buckets must also retain their own
// default encryption policy; this setting makes the application intent clear
// and protects the object even if a bucket default is later misconfigured.
func UploadEncryptedJSON(ctx context.Context, content []byte, key, bucket string) error {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return wrapStorageError("upload encrypted JSON", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, uploadTimeout)
	defer cancel()
	_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(content),
		ContentLength:        aws.Int64(int64(len(content))),
		ContentType:          aws.String("application/json"),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
	})
	return wrapStorageError("upload encrypted JSON", err)
}

// UploadStreamToS3 uploads without first materializing the complete object in
// memory. This is important for the legacy API upload path, where several
// concurrent videos would otherwise each require an additional file-sized
// byte slice before the SDK could begin sending data to S3.
func UploadStreamToS3(ctx context.Context, body io.Reader, contentLength int64, key, contentType, bucket string) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return "", wrapStorageError("upload", fmt.Errorf("S3 client is not initialized"))
	}
	if body == nil {
		return "", wrapStorageError("upload", fmt.Errorf("upload body is required"))
	}
	ctx, cancel := withStorageTimeout(ctx, uploadTimeout)
	defer cancel()
	input := &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	}
	if contentLength >= 0 {
		input.ContentLength = aws.Int64(contentLength)
	}
	var err error
	if contentLength >= 0 && contentLength <= singlePutThreshold {
		_, err = s3Client.PutObject(ctx, input)
	} else {
		//nolint:staticcheck // transfermanager migration requires a separate storage compatibility rollout.
		uploader := manager.NewUploader(s3Client, func(options *manager.Uploader) {
			options.PartSize = multipartPartSize
			options.Concurrency = 4
			options.LeavePartsOnError = false
		})
		//nolint:staticcheck // See the compatibility note on the uploader construction above.
		_, err = uploader.Upload(ctx, input)
	}
	if err != nil {
		return "", wrapStorageError("upload", err)
	}

	return GetS3URL(bucket, key), nil
}

func GetS3URL(bucket, key string) string {
	if endpoint, usePathStyle := configuration.GetS3Endpoint(); endpoint != "" {
		if objectURL, err := customEndpointObjectURL(endpoint, bucket, key, usePathStyle); err == nil {
			return objectURL
		}
	}
	region := configuration.GetS3Region()
	host := fmt.Sprintf("%s.s3.amazonaws.com", bucket)
	if region != "" {
		host = fmt.Sprintf("%s.s3.%s.amazonaws.com", bucket, region)
	}
	segments := strings.Split(strings.TrimLeft(key, "/"), "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return "https://" + host + "/" + strings.Join(segments, "/")
}

// S3KeyFromURL converts legacy CDN, S3 and s3:// object locations back to the
// canonical key used by workers. New records store keys directly, but older
// production revisions returned absolute URLs and some of those values can be
// replayed by requeue/reoptimization flows.
//
// Unknown absolute URLs are intentionally returned unchanged. Callers must
// reject them instead of accidentally treating an arbitrary host path as an
// object in the configured bucket.
func S3KeyFromURL(rawURL, bucket string) string {
	return storageObjectKeyFromURL(rawURL, bucket, os.Getenv("CDN_BASE_URL"))
}

func storageObjectKeyFromURL(rawURL, bucket, cdnBaseURL string) string {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		return strings.TrimLeft(value, "/")
	}

	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return value
	}
	if key, ok := objectKeyBelowBaseURL(parsed, cdnBaseURL); ok {
		return key
	}

	objectPath := strings.TrimLeft(parsed.Path, "/")
	if objectPath == "" {
		return value
	}
	if strings.EqualFold(parsed.Scheme, "s3") {
		if bucket != "" && !strings.EqualFold(parsed.Host, bucket) {
			return value
		}
		return objectPath
	}

	host := strings.ToLower(parsed.Hostname())
	if !isAmazonS3Host(host) {
		return value
	}
	if bucketFromHost, ok := virtualHostedS3Bucket(host); ok {
		if bucket != "" && !strings.EqualFold(bucketFromHost, bucket) {
			return value
		}
		return objectPath
	}

	parts := strings.SplitN(objectPath, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return value
	}
	if bucket != "" && !strings.EqualFold(parts[0], bucket) {
		return value
	}
	return parts[1]
}

func objectKeyBelowBaseURL(value *url.URL, rawBase string) (string, bool) {
	base, err := url.Parse(strings.TrimSpace(rawBase))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", false
	}
	if !strings.EqualFold(value.Scheme, base.Scheme) || !strings.EqualFold(value.Host, base.Host) {
		return "", false
	}
	basePath := strings.TrimRight(base.Path, "/")
	valuePath := value.Path
	if basePath != "" {
		prefix := basePath + "/"
		if !strings.HasPrefix(valuePath, prefix) {
			return "", false
		}
		valuePath = strings.TrimPrefix(valuePath, prefix)
	}
	key := strings.TrimLeft(valuePath, "/")
	return key, key != ""
}

func isAmazonS3Host(host string) bool {
	if !strings.HasSuffix(host, ".amazonaws.com") && !strings.HasSuffix(host, ".amazonaws.com.cn") {
		return false
	}
	return strings.HasPrefix(host, "s3.") || strings.HasPrefix(host, "s3-") ||
		strings.Contains(host, ".s3.") || strings.Contains(host, ".s3-")
}

func virtualHostedS3Bucket(host string) (string, bool) {
	indices := []int{strings.Index(host, ".s3."), strings.Index(host, ".s3-")}
	for _, index := range indices {
		if index > 0 {
			return host[:index], true
		}
	}
	return "", false
}

func customEndpointObjectURL(endpoint, bucket, key string, usePathStyle bool) (string, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid S3 endpoint")
	}
	objectPath := strings.TrimLeft(key, "/")
	if usePathStyle {
		base.Path = path.Join(base.Path, bucket, objectPath)
	} else {
		base.Host = bucket + "." + base.Host
		base.Path = path.Join(base.Path, objectPath)
	}
	return base.String(), nil
}

func CheckS3ObjectExists(ctx context.Context, key, bucket string) (bool, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return false, wrapStorageError("check object", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	_, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})

	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "NotFound", "NoSuchKey", "NoSuchObject":
				return false, nil
			}
		}
		var statusErr interface{ HTTPStatusCode() int }
		if errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusNotFound {
			return false, nil
		}
		return false, wrapStorageError("check object", err)
	}
	return true, nil
}

func GetS3ObjectMetadata(ctx context.Context, key, bucket string) (int64, string, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return 0, "", wrapStorageError("read object metadata", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	response, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, "", wrapStorageError("read object metadata", err)
	}
	return aws.ToInt64(response.ContentLength), aws.ToString(response.ContentType), nil
}

func DeleteS3Object(ctx context.Context, key, bucket string) error {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return wrapStorageError("delete object", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	_, err := s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return wrapStorageError("delete object", err)
}

func MarkS3ObjectUploadConfirmed(ctx context.Context, key, bucket string) error {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return wrapStorageError("confirm object upload", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	_, err := s3Client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Tagging: &s3types.Tagging{TagSet: []s3types.Tag{{
			Key:   aws.String("upload-state"),
			Value: aws.String("confirmed"),
		}}},
	})
	return wrapStorageError("confirm object upload", err)
}

func ListS3ObjectsWithPrefix(ctx context.Context, prefix, bucket string) ([]string, error) {
	var keys []string
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return nil, wrapStorageError("list objects", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, uploadTimeout)
	defer cancel()

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, wrapStorageError("list objects", err)
		}
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}

	return keys, nil
}

func GetS3Object(ctx context.Context, key, bucket string) (io.ReadCloser, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return nil, wrapStorageError("get object", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, uploadTimeout)
	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		cancel()
		return nil, wrapStorageError("get object", err)
	}
	return &cancelOnCloseReadCloser{ReadCloser: resp.Body, cancel: cancel}, nil
}

func GeneratePresignedURL(ctx context.Context, key, bucket string, expiresInMinutes int) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return "", wrapStorageError("presign download", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()

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
		return "", wrapStorageError("presign download", err)
	}

	return resp.URL, nil
}

func GeneratePresignedPutURL(ctx context.Context, key, bucket, contentType string, expiresInMinutes int) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return "", wrapStorageError("presign upload", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	input := &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
		Tagging:     aws.String(unconfirmedTagging),
	}

	presignClient := s3.NewPresignClient(s3Client)
	resp, err := presignClient.PresignPutObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(expiresInMinutes) * time.Minute
	})
	if err != nil {
		return "", wrapStorageError("presign upload", err)
	}
	return resp.URL, nil
}

func CreateMultipartUpload(ctx context.Context, key, bucket, contentType string) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return "", wrapStorageError("start multipart upload", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	resp, err := s3Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
		Tagging:     aws.String(unconfirmedTagging),
	})
	if err != nil {
		return "", wrapStorageError("start multipart upload", err)
	}
	return aws.ToString(resp.UploadId), nil
}

func GeneratePresignedUploadPartURL(ctx context.Context, key, bucket, uploadID string, partNumber, expiresInMinutes int) (string, error) {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return "", wrapStorageError("presign multipart part", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	presignClient := s3.NewPresignClient(s3Client)
	resp, err := presignClient.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(key),
		PartNumber: aws.Int32(int32(partNumber)),
		UploadId:   aws.String(uploadID),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = time.Duration(expiresInMinutes) * time.Minute
	})
	if err != nil {
		return "", wrapStorageError("presign multipart part", err)
	}
	return resp.URL, nil
}

func CompleteMultipartUpload(ctx context.Context, key, bucket, uploadID string, parts []dtos.CompletedUploadPart) error {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return wrapStorageError("complete multipart upload", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, uploadTimeout)
	defer cancel()
	completedParts := make([]s3types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completedParts = append(completedParts, s3types.CompletedPart{
			ETag:       aws.String(part.ETag),
			PartNumber: aws.Int32(int32(part.PartNumber)),
		})
	}

	_, err := s3Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	if isMultipartUploadNotFound(err) {
		return fmt.Errorf("%w: %w", ports.ErrMultipartUploadNotFound, wrapStorageError("complete multipart upload", err))
	}
	return wrapStorageError("complete multipart upload", err)
}

func isMultipartUploadNotFound(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchUpload"
}

func AbortMultipartUpload(ctx context.Context, key, bucket, uploadID string) error {
	s3Client := configuration.GetS3Client(nil)
	if s3Client == nil {
		return wrapStorageError("abort multipart upload", fmt.Errorf("S3 client is not initialized"))
	}
	ctx, cancel := withStorageTimeout(ctx, metadataTimeout)
	defer cancel()
	_, err := s3Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	return wrapStorageError("abort multipart upload", err)
}
