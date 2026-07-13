package designtemplates

import (
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockDesignTemplateStorage struct{}

func (m *mockDesignTemplateStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return true, "", nil
}

func (m *mockDesignTemplateStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	return "https://signed.example.com/" + folder + "/" + filename, nil
}

func (m *mockDesignTemplateStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return "", nil
}

func (m *mockDesignTemplateStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return "", nil
}

func (m *mockDesignTemplateStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}

func (m *mockDesignTemplateStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return nil
}

func (m *mockDesignTemplateStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}

func (m *mockDesignTemplateStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return "", nil
}

func (m *mockDesignTemplateStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	return nil
}

func (m *mockDesignTemplateStorage) DeleteFile(filename, folder, bucket, provider string) error {
	return nil
}

func (m *mockDesignTemplateStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

var _ ports.ObjectStorageRepository = (*mockDesignTemplateStorage)(nil)

func TestWithDesignTemplatePreviewViewURLPreservesRawPreviewAndAddsSignedAlias(t *testing.T) {
	origResourceSvc := designTemplateResourceSvc
	designTemplateResourceSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockDesignTemplateStorage{}},
	)
	t.Cleanup(func() { designTemplateResourceSvc = origResourceSvc })

	response := withDesignTemplatePreviewViewURL(dtos.DesignTemplateResponse{
		PreviewURL:      "base/templates/modern.webp",
		PreviewImageURL: "base/templates/modern.webp",
	})

	assert.Equal(t, "base/templates/modern.webp", response.PreviewURL)
	assert.Equal(t, "base/templates/modern.webp", response.PreviewImageURL)
	assert.Equal(t, "https://signed.example.com/base/templates/modern.webp", response.PreviewViewURL)
	require.NotNil(t, response.PreviewViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(resourcesService.ResourceViewURLTTLMinutes*time.Minute), *response.PreviewViewURLExpiresAt, 2*time.Second)
}

func TestFontSetResponseWithFontViewURLsPreservesRawFontURLAndAddsSignedAlias(t *testing.T) {
	origResourceSvc := designTemplateResourceSvc
	designTemplateResourceSvc = resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Storage: &mockDesignTemplateStorage{}},
	)
	t.Cleanup(func() { designTemplateResourceSvc = origResourceSvc })

	response := fontSetResponseWithFontViewURLs(dtos.FontSetResponse{
		Patterns: []dtos.FontSetPatternResponse{
			{
				Key: "heading",
				Font: &dtos.FontResponse{
					Name:   "Cormorant Garamond",
					Family: "Cormorant Garamond",
					URL:    "base/fonts/cormorant.woff2",
				},
			},
		},
	})

	require.Len(t, response.Patterns, 1)
	require.NotNil(t, response.Patterns[0].Font)
	font := response.Patterns[0].Font
	assert.Equal(t, "base/fonts/cormorant.woff2", font.URL)
	assert.Equal(t, "https://signed.example.com/base/fonts/cormorant.woff2", font.ViewURL)
	require.NotNil(t, font.ViewURLExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(resourcesService.ResourceViewURLTTLMinutes*time.Minute), *font.ViewURLExpiresAt, 2*time.Second)
}
