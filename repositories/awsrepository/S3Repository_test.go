package awsrepository

import (
	"context"
	"errors"
	"testing"
	"time"

	"events-stocks/configuration"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapStorageErrorClassifiesPermanentRedirect(t *testing.T) {
	err := wrapStorageError("upload", &smithy.GenericAPIError{
		Code:    "PermanentRedirect",
		Message: "use the specified endpoint",
	})
	var storageErr *StorageError
	require.ErrorAs(t, err, &storageErr)
	assert.Equal(t, "region", storageErr.Kind)
	assert.False(t, storageErr.Temporary())
	assert.NotContains(t, storageErr.ClientMessage(), "specified endpoint")
}

func TestWrapStorageErrorClassifiesTimeout(t *testing.T) {
	err := wrapStorageError("upload", context.DeadlineExceeded)
	var storageErr *StorageError
	require.ErrorAs(t, err, &storageErr)
	assert.Equal(t, "timeout", storageErr.Kind)
	assert.True(t, storageErr.Temporary())
}

func TestGetS3URLUsesResolvedRegionAndEscapesKey(t *testing.T) {
	previous := configuration.GetS3Region()
	previousEndpoint, previousPathStyle := configuration.GetS3Endpoint()
	t.Cleanup(func() {
		configuration.SetS3Region(previous)
		configuration.SetS3Endpoint(previousEndpoint, previousPathStyle)
	})
	configuration.SetS3Region("us-east-2")
	configuration.SetS3Endpoint("", false)

	assert.Equal(t,
		"https://event-media.s3.us-east-2.amazonaws.com/events/photo%20one.webp",
		GetS3URL("event-media", "events/photo one.webp"),
	)
}

func TestGetS3URLUsesCustomPathStyleEndpoint(t *testing.T) {
	previousEndpoint, previousPathStyle := configuration.GetS3Endpoint()
	t.Cleanup(func() { configuration.SetS3Endpoint(previousEndpoint, previousPathStyle) })
	configuration.SetS3Endpoint("http://localhost:4566/storage", true)

	assert.Equal(t,
		"http://localhost:4566/storage/event-media/events/photo%20one.webp",
		GetS3URL("event-media", "events/photo one.webp"),
	)
}

func TestGetS3URLUsesCustomVirtualHostEndpoint(t *testing.T) {
	previousEndpoint, previousPathStyle := configuration.GetS3Endpoint()
	t.Cleanup(func() { configuration.SetS3Endpoint(previousEndpoint, previousPathStyle) })
	configuration.SetS3Endpoint("https://objects.example.com/base", false)

	assert.Equal(t,
		"https://event-media.objects.example.com/base/events/photo.webp",
		GetS3URL("event-media", "events/photo.webp"),
	)
}

func TestIsMultipartUploadNotFound(t *testing.T) {
	assert.True(t, isMultipartUploadNotFound(&smithy.GenericAPIError{Code: "NoSuchUpload"}))
	assert.False(t, isMultipartUploadNotFound(&smithy.GenericAPIError{Code: "AccessDenied"}))
}

func TestWithStorageTimeoutPreservesCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, gotCancel := withStorageTimeout(ctx, time.Minute)
	defer gotCancel()
	wantDeadline, _ := ctx.Deadline()
	gotDeadline, _ := got.Deadline()
	assert.WithinDuration(t, wantDeadline, gotDeadline, time.Millisecond)
}

func TestStorageErrorUnwrap(t *testing.T) {
	cause := errors.New("cause")
	assert.ErrorIs(t, &StorageError{Operation: "upload", Kind: "unavailable", Err: cause}, cause)
}

func TestStorageObjectKeyFromURLNormalizesTrustedLocations(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		bucket  string
		cdnBase string
		want    string
	}{
		{name: "bare key", value: "/moments/event/raw/photo.jpg", bucket: "media", want: "moments/event/raw/photo.jpg"},
		{name: "configured CDN", value: "https://cdn.eventiapp.com.mx/media/moments/event/photo.webp", bucket: "media", cdnBase: "https://cdn.eventiapp.com.mx/media", want: "moments/event/photo.webp"},
		{name: "virtual hosted regional S3", value: "https://media.s3.us-east-2.amazonaws.com/moments/event/photo.webp?x=1", bucket: "media", want: "moments/event/photo.webp"},
		{name: "virtual hosted legacy S3", value: "https://media.s3.amazonaws.com/moments/event/photo.webp", bucket: "media", want: "moments/event/photo.webp"},
		{name: "path style S3", value: "https://s3.us-east-2.amazonaws.com/media/moments/event/photo.webp", bucket: "media", want: "moments/event/photo.webp"},
		{name: "s3 scheme", value: "s3://media/moments/event/photo.webp", bucket: "media", want: "moments/event/photo.webp"},
		{name: "foreign CDN remains absolute", value: "https://cdn.example.com/moments/event/photo.webp", bucket: "media", cdnBase: "https://cdn.eventiapp.com.mx", want: "https://cdn.example.com/moments/event/photo.webp"},
		{name: "foreign S3 bucket remains absolute", value: "https://other.s3.us-east-2.amazonaws.com/moments/event/photo.webp", bucket: "media", want: "https://other.s3.us-east-2.amazonaws.com/moments/event/photo.webp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, storageObjectKeyFromURL(tt.value, tt.bucket, tt.cdnBase))
		})
	}
}
