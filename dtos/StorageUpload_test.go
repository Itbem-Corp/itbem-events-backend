package dtos

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompletedUploadPartAcceptsCommonJSONAliases(t *testing.T) {
	var parts []CompletedUploadPart
	err := json.Unmarshal([]byte(`[
		{"part_number":1,"etag":"etag-1"},
		{"partNumber":2,"ETag":"etag-2"},
		{"PartNumber":3,"eTag":"etag-3"},
		{"part_number":4,"partNumber":99,"etag":"canonical-wins","ETag":"ignored"}
	]`), &parts)

	require.NoError(t, err)
	require.Len(t, parts, 4)
	assert.Equal(t, CompletedUploadPart{PartNumber: 1, ETag: "etag-1"}, parts[0])
	assert.Equal(t, CompletedUploadPart{PartNumber: 2, ETag: "etag-2"}, parts[1])
	assert.Equal(t, CompletedUploadPart{PartNumber: 3, ETag: "etag-3"}, parts[2])
	assert.Equal(t, CompletedUploadPart{PartNumber: 4, ETag: "canonical-wins"}, parts[3])
}

func TestNormalizeCompletedUploadPartsSortsTrimsAndDropsInvalidEntries(t *testing.T) {
	parts := NormalizeCompletedUploadParts([]CompletedUploadPart{
		{PartNumber: 2, ETag: " etag-2 "},
		{PartNumber: 0, ETag: "etag-0"},
		{PartNumber: 1, ETag: "etag-1"},
		{PartNumber: 2, ETag: "etag-2-duplicate"},
		{PartNumber: 3, ETag: " "},
	})

	assert.Equal(t, []CompletedUploadPart{
		{PartNumber: 1, ETag: "etag-1"},
		{PartNumber: 2, ETag: "etag-2"},
	}, parts)
}

func TestNewSharedMultipartUploadStartResponseNormalizesPartURLs(t *testing.T) {
	response := NewSharedMultipartUploadStartResponse(
		"upload-1",
		"moments/event/raw/video.mp4",
		[]PresignedUploadPart{
			{PartNumber: 2, URL: " https://s3.example.com/part-2 "},
			{PartNumber: 1, URL: "https://s3.example.com/part-1"},
			{PartNumber: 2, URL: "https://s3.example.com/part-2-duplicate"},
			{PartNumber: 3, URL: " "},
			{PartNumber: 0, URL: "https://s3.example.com/part-0"},
		},
		"video/mp4",
	)

	require.Len(t, response.PartURLs, 2)
	assert.Equal(t, []PresignedUploadPart{
		{PartNumber: 1, URL: "https://s3.example.com/part-1"},
		{PartNumber: 2, URL: "https://s3.example.com/part-2"},
	}, response.PartURLs)
	assert.Equal(t, response.ObjectKey, response.S3Key)
}
