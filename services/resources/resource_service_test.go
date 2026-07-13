package services

import (
	"bytes"
	"context"
	"errors"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/services/ports"
	"events-stocks/utils"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/textproto"
	"testing"
	"time"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memoryMultipartFile struct {
	*bytes.Reader
}

func (m *memoryMultipartFile) Close() error { return nil }

func newTestFileHeader(filename, contentType string, size int64) *multipart.FileHeader {
	return &multipart.FileHeader{
		Filename: filename,
		Header: textproto.MIMEHeader{
			"Content-Type": []string{contentType},
		},
		Size: size,
	}
}

func validTestJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

type mockResourceRepo struct {
	resource                 *models.Resource
	createCalls              int
	deletedID                uuid.UUID
	versionedDeleteCalls     int
	versionedDeleteSectionID uuid.UUID
	versionedDeleteAt        time.Time
	updateCalls              int
	touchCalls               int
	touchedID                uuid.UUID
	touchedAt                time.Time
	listCalls                int
	listResourceTypeCalls    int
	listResourcesResponse    []models.Resource
	resourceTypesResponse    []models.ResourceType
}

func (m *mockResourceRepo) CreateResource(resource *models.Resource) error {
	m.createCalls++
	return nil
}
func (m *mockResourceRepo) UpdateResource(resource *models.Resource) error {
	m.updateCalls++
	return nil
}
func (m *mockResourceRepo) TouchResourceUpdatedAt(id uuid.UUID, updatedAt time.Time) error {
	m.touchCalls++
	m.touchedID = id
	m.touchedAt = updatedAt
	return nil
}
func (m *mockResourceRepo) DeleteResource(id uuid.UUID) error {
	m.deletedID = id
	return nil
}
func (m *mockResourceRepo) DeleteResourceAndTouchSection(resourceID uuid.UUID, sectionID uuid.UUID, updatedAt time.Time) error {
	m.versionedDeleteCalls++
	m.deletedID = resourceID
	m.versionedDeleteSectionID = sectionID
	m.versionedDeleteAt = updatedAt
	return nil
}
func (m *mockResourceRepo) GetResourceByID(id uuid.UUID) (*models.Resource, error) {
	if m.resource != nil {
		return m.resource, nil
	}
	return &models.Resource{ID: id}, nil
}
func (m *mockResourceRepo) ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error) {
	m.listCalls++
	return m.listResourcesResponse, nil
}
func (m *mockResourceRepo) ListResourceTypesRaw() ([]models.ResourceType, error) {
	m.listResourceTypeCalls++
	return m.resourceTypesResponse, nil
}

var _ ports.ResourceRepository = (*mockResourceRepo)(nil)

type mockCacheRepo struct {
	invalidations []string
	invalidateErr error
	getKey        string
	saveKey       string
	saveValue     string
	saveTTL       time.Duration
}

func (m *mockCacheRepo) Invalidate(resource string, key string) error {
	m.invalidations = append(m.invalidations, key+":"+resource)
	return m.invalidateErr
}
func (m *mockCacheRepo) DeleteKeysByPattern(ctx context.Context, pattern string) error { return nil }
func (m *mockCacheRepo) GetKey(ctx context.Context, key string) (string, error) {
	m.getKey = key
	return "", nil
}
func (m *mockCacheRepo) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	m.saveKey = key
	m.saveValue = value
	m.saveTTL = ttl
	return nil
}

var _ ports.CacheRepository = (*mockCacheRepo)(nil)

type mockObjectStorage struct {
	exists              bool
	calls               []string
	deletedFilename     string
	deletedFolder       string
	uploadedFilename    string
	uploadedFolder      string
	uploadedContentType string
	updatedFilename     string
	updatedFolder       string
	completedObjectKey  string
	completedUploadID   string
	completedParts      []dtos.CompletedUploadPart
	completeErr         error
	lastPresignMinutes  int
	lastPresignFilename string
	lastPresignFolder   string
	lastPresignKey      string
	lastPresignType     string
	multipartKey        string
	multipartType       string
}

func (m *mockObjectStorage) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	m.calls = append(m.calls, "exists:"+filename)
	return m.exists, "", nil
}
func (m *mockObjectStorage) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	m.lastPresignMinutes = minutes
	m.lastPresignFilename = filename
	m.lastPresignFolder = folder
	return "https://signed.example.com/" + folder + "/" + filename, nil
}
func (m *mockObjectStorage) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	m.lastPresignKey = objectKey
	m.lastPresignType = contentType
	return "", nil
}
func (m *mockObjectStorage) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	m.multipartKey = objectKey
	m.multipartType = contentType
	return "", nil
}
func (m *mockObjectStorage) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return "", nil
}
func (m *mockObjectStorage) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	m.completedObjectKey = objectKey
	m.completedUploadID = uploadID
	m.completedParts = append([]dtos.CompletedUploadPart(nil), parts...)
	return m.completeErr
}
func (m *mockObjectStorage) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return nil
}
func (m *mockObjectStorage) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	m.updatedFilename = filename
	m.updatedFolder = folder
	return "", nil
}
func (m *mockObjectStorage) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	m.calls = append(m.calls, "upload:"+filename)
	m.uploadedFilename = filename
	m.uploadedFolder = folder
	m.uploadedContentType = contentType
	return nil
}
func (m *mockObjectStorage) DeleteFile(filename, folder, bucket, provider string) error {
	m.calls = append(m.calls, "delete:"+filename)
	m.deletedFilename = filename
	m.deletedFolder = folder
	return nil
}
func (m *mockObjectStorage) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

var _ ports.ObjectStorageRepository = (*mockObjectStorage)(nil)

type metadataObjectStorage struct {
	*mockObjectStorage
	metadata    ports.ObjectStorageMetadata
	metadataErr error
}

func (m *metadataObjectStorage) GetObjectMetadata(filename, folder, bucket, provider string) (ports.ObjectStorageMetadata, error) {
	m.calls = append(m.calls, "metadata:"+folder+"/"+filename)
	return m.metadata, m.metadataErr
}

var _ ports.ObjectStorageMetadataReader = (*metadataObjectStorage)(nil)

func newTestResourceService(repo ports.ResourceRepository, cache ports.CacheRepository, storage ports.ObjectStorageRepository) *ResourceService {
	return NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		ResourceServiceDeps{Repo: repo, Cache: cache, Storage: storage},
	)
}

func TestVerifyMomentUploadUsesStoredMetadata(t *testing.T) {
	storage := &metadataObjectStorage{
		mockObjectStorage: &mockObjectStorage{},
		metadata: ports.ObjectStorageMetadata{
			Size:        2 * 1024 * 1024,
			ContentType: "image/jpeg",
		},
	}
	svc := newTestResourceService(nil, nil, storage)

	contentType, err := svc.VerifyMomentUpload("moments/event-1/raw/photo.jpg", "image/jpeg")
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", contentType)
	assert.Equal(t, []string{"metadata:moments/event-1/raw/photo.jpg"}, storage.calls)
}

func TestVerifyMomentUploadRejectsOversizedStoredObject(t *testing.T) {
	storage := &metadataObjectStorage{
		mockObjectStorage: &mockObjectStorage{},
		metadata: ports.ObjectStorageMetadata{
			Size:        int64(MaxMomentImageFileSizeBytes) + 1,
			ContentType: "image/jpeg",
		},
	}
	svc := newTestResourceService(nil, nil, storage)

	_, err := svc.VerifyMomentUpload("moments/event-1/raw/photo.jpg", "image/jpeg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file size exceeds 25 MB")
}

func TestVerifyMomentUploadRejectsContentTypeMismatch(t *testing.T) {
	storage := &metadataObjectStorage{
		mockObjectStorage: &mockObjectStorage{},
		metadata: ports.ObjectStorageMetadata{
			Size:        1024,
			ContentType: "video/mp4",
		},
	}
	svc := newTestResourceService(nil, nil, storage)

	_, err := svc.VerifyMomentUpload("moments/event-1/raw/photo.jpg", "image/jpeg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "content type does not match")
}

func TestReplaceFileUploadsNewObjectBeforeOldObjectCleanup(t *testing.T) {
	storage := &mockObjectStorage{exists: true}
	svc := newTestResourceService(nil, nil, storage)
	content := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)

	path, err := svc.ReplaceFile(
		"old.svg",
		&memoryMultipartFile{Reader: bytes.NewReader(content)},
		newTestFileHeader("new.svg", "image/svg+xml", int64(len(content))),
	)

	require.NoError(t, err)
	assert.NotEqual(t, "events/old.svg", path)
	assert.NotEmpty(t, storage.uploadedFilename)
	assert.Equal(t, "events", storage.uploadedFolder)
	assert.Empty(t, storage.deletedFilename)
	assert.Equal(t, []string{"upload:" + storage.uploadedFilename}, storage.calls)
}

func TestReplaceFilePreservesStoredNestedFolder(t *testing.T) {
	storage := &mockObjectStorage{exists: true}
	svc := newTestResourceService(nil, nil, storage)
	content := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)

	path, err := svc.ReplaceFile(
		"events/base/hero/old.svg",
		&memoryMultipartFile{Reader: bytes.NewReader(content)},
		newTestFileHeader("new.svg", "image/svg+xml", int64(len(content))),
	)

	require.NoError(t, err)
	assert.Equal(t, "events/base/hero", storage.uploadedFolder)
	assert.Equal(t, "events/base/hero/"+storage.uploadedFilename, path)
}

func TestUpdateFileContentPreservesStoredNestedFolder(t *testing.T) {
	storage := &mockObjectStorage{exists: true}
	svc := newTestResourceService(nil, nil, storage)
	content := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)

	path, err := svc.UpdateFileContent(
		&memoryMultipartFile{Reader: bytes.NewReader(content)},
		"events/base/hero/photo.svg",
		newTestFileHeader("photo.svg", "image/svg+xml", int64(len(content))),
	)

	require.NoError(t, err)
	assert.Equal(t, "photo.svg", storage.updatedFilename)
	assert.Equal(t, "events/base/hero", storage.updatedFolder)
	assert.Equal(t, "events/base/hero/photo.svg", path)
}

func TestUploadFilenameExtensionContractCoversAllowedModernMedia(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		expected    string
	}{
		{name: "heic image", contentType: "image/heic", expected: "asset.heic"},
		{name: "heif image", contentType: "image/heif", expected: "asset.heif"},
		{name: "avif image", contentType: "image/avif", expected: "asset.avif"},
		{name: "mobile m4v video", contentType: "video/x-m4v", expected: "asset.m4v"},
		{name: "mobile 3gp video", contentType: "video/3gpp", expected: "asset.3gp"},
		{name: "avi video", contentType: "video/x-msvideo", expected: "asset.avi"},
		{name: "matroska video", contentType: "video/x-matroska", expected: "asset.mkv"},
		{name: "mp3 audio", contentType: "audio/mpeg", expected: "asset.mp3"},
		{name: "ogg audio", contentType: "audio/ogg", expected: "asset.ogg"},
		{name: "wav audio", contentType: "audio/wav", expected: "asset.wav"},
		{name: "aac audio", contentType: "audio/aac", expected: "asset.aac"},
		{name: "flac audio", contentType: "audio/flac", expected: "asset.flac"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, AllowedMimeTypes[tc.contentType], "test case must stay aligned with backend allowed MIME types")
			assert.Equal(t, tc.expected, updateFilenameExtension("asset.original", tc.contentType))
		})
	}
}

func TestGuessMimeTypeDistinguishesHeicAndHeif(t *testing.T) {
	assert.Equal(t, "image/heic", guessMimeType("heic"))
	assert.Equal(t, "image/heif", guessMimeType("heif"))
	assert.Equal(t, "image/avif", guessMimeType("avif"))
}

func TestNormalizeMomentUploadContentTypeMatchesPublicUploadPolicy(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		contentType string
		expected    string
	}{
		{name: "jpg alias", filename: "foto.bin", contentType: "image/jpg", expected: "image/jpeg"},
		{name: "jpeg extension", filename: "foto.jpeg", contentType: "", expected: "image/jpeg"},
		{name: "mobile mov extension", filename: "clip.MOV", contentType: "", expected: "video/quicktime"},
		{name: "avi extension", filename: "clip.avi", contentType: "", expected: "video/x-msvideo"},
		{name: "matroska extension", filename: "clip.mkv", contentType: "", expected: "video/x-matroska"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contentType, err := normalizeMomentUploadContentType(tc.filename, tc.contentType)

			require.NoError(t, err)
			assert.Equal(t, tc.expected, contentType)
		})
	}
}

func TestNormalizeMomentUploadContentTypeRejectsUnsupportedMomentMedia(t *testing.T) {
	cases := []struct {
		name        string
		filename    string
		contentType string
	}{
		{name: "svg is not a public moment image", filename: "vector.svg", contentType: ""},
		{name: "unsupported video wildcard", filename: "clip.flv", contentType: "video/x-flv"},
		{name: "audio is not a public moment media type", filename: "song.mp3", contentType: ""},
		{name: "unknown extension", filename: "archive.bin", contentType: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeMomentUploadContentType(tc.filename, tc.contentType)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsupported file type for moments")
		})
	}
}

func TestUploadRawToMomentsFolderNormalizesMomentContentTypeBeforeStorage(t *testing.T) {
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	key, contentType, err := svc.UploadRawToMomentsFolder(
		&memoryMultipartFile{Reader: bytes.NewReader([]byte("jpg-bytes"))},
		newTestFileHeader("foto.bin", "image/jpg", int64(len("jpg-bytes"))),
		"event-1",
	)

	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", contentType)
	assert.Equal(t, "image/jpeg", storage.uploadedContentType)
	assert.Contains(t, key, "moments/event-1/raw/")
}

func TestUploadClientLogoNormalizesJpgAliasBeforeStorage(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)
	jpegBytes := validTestJPEG(t)

	_, _, err := svc.UploadClientLogo(
		&memoryMultipartFile{Reader: bytes.NewReader(jpegBytes)},
		newTestFileHeader("logo.bin", "image/jpg", int64(len(jpegBytes))),
		clientID,
	)

	require.NoError(t, err)
	assert.Contains(t, []string{"image/jpeg", "image/webp"}, storage.uploadedContentType)
	assert.NotEqual(t, "image/jpg", storage.uploadedContentType)
	assert.Equal(t, "clients/"+clientID.String()+"/logo", storage.uploadedFolder)
}

func TestPrepareMomentUploadURLRejectsUnsupportedMomentMedia(t *testing.T) {
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	_, _, _, err := svc.PrepareMomentUploadURL("event-1", "vector.svg", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported file type for moments: image/svg+xml")
	assert.Empty(t, storage.lastPresignKey)
}

func TestUploadClientLogoRejectsNonImageMedia(t *testing.T) {
	clientID := uuid.Must(uuid.NewV4())
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	_, _, err := svc.UploadClientLogo(
		&memoryMultipartFile{Reader: bytes.NewReader([]byte("video-bytes"))},
		newTestFileHeader("logo.mp4", "video/mp4", int64(len("video-bytes"))),
		clientID,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported image type: video/mp4")
	assert.Empty(t, storage.uploadedFilename)
}

func TestUploadAvatarRejectsNonImageMedia(t *testing.T) {
	userID := uuid.Must(uuid.NewV4())
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	_, err := svc.UploadAvatar(
		&memoryMultipartFile{Reader: bytes.NewReader([]byte("audio-bytes"))},
		newTestFileHeader("avatar.mp3", "audio/mpeg", int64(len("audio-bytes"))),
		userID,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported image type: audio/mpeg")
	assert.Empty(t, storage.uploadedFilename)
}

func TestDeleteResourceDeletesRecordWhenObjectAlreadyMissing(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:             resourceID,
			EventSectionID: &sectionID,
			Path:           "events/missing.webp",
		},
	}
	cache := &mockCacheRepo{}
	storage := &mockObjectStorage{exists: false}
	svc := newTestResourceService(repo, cache, storage)

	err := svc.DeleteResource(resourceID)

	require.NoError(t, err)
	assert.Equal(t, resourceID, repo.deletedID)
	assert.Empty(t, storage.deletedFilename)
	assert.Contains(t, cache.invalidations, sectionID.String()+":resources")
	assert.Zero(t, repo.listCalls)
	assert.Zero(t, repo.updateCalls)
}

func TestDeleteResourceTouchesSectionVersionForPublicPageSpec(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:             resourceID,
			EventSectionID: &sectionID,
			Path:           "events/gallery/photo.webp",
		},
	}
	cache := &mockCacheRepo{}
	storage := &mockObjectStorage{exists: false}
	svc := newTestResourceService(repo, cache, storage)

	err := svc.DeleteResource(resourceID)

	require.NoError(t, err)
	assert.Equal(t, 1, repo.versionedDeleteCalls)
	assert.Equal(t, resourceID, repo.deletedID)
	assert.Equal(t, sectionID, repo.versionedDeleteSectionID)
	assert.False(t, repo.versionedDeleteAt.IsZero())
	assert.Contains(t, cache.invalidations, sectionID.String()+":resources")
}

func TestDeleteResourceDeletesStoredNestedObjectPath(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:             resourceID,
			EventSectionID: &sectionID,
			Path:           "events/base/hero/photo.webp",
		},
	}
	cache := &mockCacheRepo{}
	storage := &mockObjectStorage{exists: true}
	svc := newTestResourceService(repo, cache, storage)

	err := svc.DeleteResource(resourceID)

	require.NoError(t, err)
	assert.Equal(t, resourceID, repo.deletedID)
	assert.Equal(t, "photo.webp", storage.deletedFilename)
	assert.Equal(t, "events/base/hero", storage.deletedFolder)
	assert.Contains(t, cache.invalidations, sectionID.String()+":resources")
}

func TestGetResourcesBySectionIDIncludesViewURLExpiry(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	position := 2
	repo := &mockResourceRepo{
		listResourcesResponse: []models.Resource{
			{
				ID:             resourceID,
				EventSectionID: &sectionID,
				ResourceTypeID: resourceTypeID,
				Path:           "events/photo.webp",
				Title:          "Foto principal",
				Position:       &position,
			},
		},
	}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, &mockCacheRepo{}, storage)
	before := time.Now().UTC()

	resources, err := svc.GetResourcesBySectionID(sectionID)

	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, ResourceViewURLTTLMinutes, storage.lastPresignMinutes)
	assert.Equal(t, "https://signed.example.com/events/photo.webp", resources[0].ViewURL)
	require.NotNil(t, resources[0].ViewURLExpiresAt)
	assert.True(t, resources[0].ViewURLExpiresAt.After(before))
}

func TestGetResourcesBySectionIDSortsByPositionWithStableTieBreak(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	firstID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000001"))
	secondID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000002"))
	thirdID := uuid.Must(uuid.FromString("00000000-0000-0000-0000-000000000003"))
	positionOne := 1
	positionTwo := 2
	repo := &mockResourceRepo{
		listResourcesResponse: []models.Resource{
			{ID: thirdID, EventSectionID: &sectionID, ResourceTypeID: resourceTypeID, Path: "events/third.webp", Position: &positionTwo, Title: "Third"},
			{ID: secondID, EventSectionID: &sectionID, ResourceTypeID: resourceTypeID, Path: "events/second.webp", Position: &positionOne, Title: "Second"},
			{ID: firstID, EventSectionID: &sectionID, ResourceTypeID: resourceTypeID, Path: "events/first.webp", Position: &positionOne, Title: "First"},
		},
	}
	svc := newTestResourceService(repo, nil, &mockObjectStorage{exists: true})

	resources, err := svc.GetResourcesBySectionID(sectionID)

	require.NoError(t, err)
	require.Len(t, resources, 3)
	assert.Equal(t, []uuid.UUID{firstID, secondID, thirdID}, []uuid.UUID{
		resources[0].ID,
		resources[1].ID,
		resources[2].ID,
	})
	assert.Equal(t, []string{"First", "Second", "Third"}, []string{
		resources[0].Title,
		resources[1].Title,
		resources[2].Title,
	})
}

func TestGetResourcesBySectionIDSignsStoredNestedResourcePath(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		listResourcesResponse: []models.Resource{
			{
				ID:             resourceID,
				EventSectionID: &sectionID,
				Path:           "events/base/hero/photo.webp",
				Title:          "Foto con subfolder",
			},
		},
	}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, &mockCacheRepo{}, storage)

	resources, err := svc.GetResourcesBySectionID(sectionID)

	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "events/base/hero", storage.lastPresignFolder)
	assert.Equal(t, "photo.webp", storage.lastPresignFilename)
	assert.Equal(t, "https://signed.example.com/events/base/hero/photo.webp", resources[0].ViewURL)
}

func TestGetResourcesBySectionIDPreservesAbsoluteURLLikeResourcePaths(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		listResourcesResponse: []models.Resource{
			{
				ID:             resourceID,
				EventSectionID: &sectionID,
				Path:           "blob:https://app.example.com/local-preview",
				Title:          "Preview",
			},
		},
	}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, &mockCacheRepo{}, storage)

	resources, err := svc.GetResourcesBySectionID(sectionID)

	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "blob:https://app.example.com/local-preview", resources[0].ViewURL)
	assert.Nil(t, resources[0].ViewURLExpiresAt)
	assert.Zero(t, storage.lastPresignMinutes)
}

func TestGetResourceByIDSignsStoredNestedResourcePath(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:    resourceID,
			Path:  "events/base/hero/photo.webp",
			Title: "Foto con subfolder",
		},
	}
	storage := &mockObjectStorage{exists: true}
	svc := newTestResourceService(repo, &mockCacheRepo{}, storage)

	resource, viewURL, err := svc.GetResourceByID(resourceID)

	require.NoError(t, err)
	require.NotNil(t, resource)
	assert.Equal(t, "events/base/hero", storage.lastPresignFolder)
	assert.Equal(t, "photo.webp", storage.lastPresignFilename)
	assert.Equal(t, "https://signed.example.com/events/base/hero/photo.webp", viewURL)
}

func TestGetResourceByIDPreservesAbsoluteURLLikePathWithoutStorage(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:    resourceID,
			Path:  "//cdn.example.com/events/photo.webp",
			Title: "Foto externa",
		},
	}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, &mockCacheRepo{}, storage)

	resource, viewURL, err := svc.GetResourceByID(resourceID)

	require.NoError(t, err)
	require.NotNil(t, resource)
	assert.Equal(t, "//cdn.example.com/events/photo.webp", viewURL)
	assert.Empty(t, storage.calls)
	assert.Zero(t, storage.lastPresignMinutes)
}

func TestGetAdminResourcesBySectionIDIncludesPathWithoutPublicCache(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	resourceTypeID := uuid.Must(uuid.NewV4())
	position := 2
	updatedAt := time.Date(2026, 7, 5, 19, 0, 0, 0, time.UTC)
	repo := &mockResourceRepo{
		listResourcesResponse: []models.Resource{
			{
				ID:             resourceID,
				EventSectionID: &sectionID,
				ResourceTypeID: resourceTypeID,
				Path:           "events/admin-photo.webp",
				Title:          "Foto interna",
				Position:       &position,
				UpdatedAt:      updatedAt,
			},
		},
	}
	cache := &mockCacheRepo{}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, cache, storage)

	resources, err := svc.GetAdminResourcesBySectionID(sectionID)

	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "events/admin-photo.webp", resources[0].Path)
	assert.Equal(t, updatedAt, resources[0].UpdatedAt)
	assert.Equal(t, "https://signed.example.com/events/admin-photo.webp", resources[0].ViewURL)
	assert.Equal(t, ResourceViewURLTTLMinutes, storage.lastPresignMinutes)
	assert.Empty(t, cache.getKey)
	assert.Empty(t, cache.saveKey)
}

func TestGetResourcesBySectionIDCachesSignedResourcesWithScopedKey(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	resourceID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		listResourcesResponse: []models.Resource{
			{
				ID:             resourceID,
				EventSectionID: &sectionID,
				Path:           "events/photo.webp",
				Title:          "Foto principal",
			},
		},
	}
	cache := &mockCacheRepo{}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, cache, storage)

	resources, err := svc.GetResourcesBySectionID(sectionID)

	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, 1, repo.listCalls)
	expectedKey := sectionID.String() + ":resources"
	assert.Equal(t, expectedKey, cache.getKey)
	assert.Equal(t, expectedKey, cache.saveKey)
	assert.Equal(t, "https://signed.example.com/events/photo.webp", resources[0].ViewURL)
	assert.Contains(t, cache.saveValue, `"view_url":"https://signed.example.com/events/photo.webp"`)
	assert.Positive(t, cache.saveTTL)
}

func TestGetResourceRecordByIDUsesRepositoryWithoutSigningURL(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:    resourceID,
			Path:  "events/private.webp",
			Title: "Privado",
		},
	}
	storage := &mockObjectStorage{}
	svc := newTestResourceService(repo, &mockCacheRepo{}, storage)

	resource, err := svc.GetResourceRecordByID(resourceID)

	require.NoError(t, err)
	require.NotNil(t, resource)
	assert.Equal(t, resourceID, resource.ID)
	assert.Empty(t, storage.calls)
	assert.Zero(t, storage.lastPresignMinutes)
}

func TestInvalidateSectionResourceCacheInvalidatesScopedResources(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	cache := &mockCacheRepo{}
	svc := newTestResourceService(nil, cache, nil)

	err := svc.InvalidateSectionResourceCache(&sectionID)

	require.NoError(t, err)
	assert.Equal(t, []string{sectionID.String() + ":resources"}, cache.invalidations)
}

func TestInvalidateSectionResourceCacheSkipsUnscopedResources(t *testing.T) {
	svc := newTestResourceService(nil, nil, nil)

	err := svc.InvalidateSectionResourceCache(nil)

	require.NoError(t, err)
}

func TestInvalidateSectionResourceCacheAllowsNilCache(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	svc := newTestResourceService(nil, nil, nil)

	err := svc.InvalidateSectionResourceCache(&sectionID)

	require.NoError(t, err)
}

func TestCreateResourceIgnoresCacheInvalidationError(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{}
	cache := &mockCacheRepo{invalidateErr: errors.New("redis unavailable")}
	svc := newTestResourceService(repo, cache, nil)

	err := svc.CreateResource(&models.Resource{EventSectionID: &sectionID})

	require.NoError(t, err)
	assert.Equal(t, 1, repo.createCalls)
	assert.Equal(t, []string{sectionID.String() + ":resources"}, cache.invalidations)
}

func TestUpdateResourceIgnoresCacheInvalidationError(t *testing.T) {
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{}
	cache := &mockCacheRepo{invalidateErr: errors.New("redis unavailable")}
	svc := newTestResourceService(repo, cache, nil)

	err := svc.UpdateResource(&models.Resource{EventSectionID: &sectionID})

	require.NoError(t, err)
	assert.Equal(t, 1, repo.updateCalls)
	assert.Equal(t, []string{sectionID.String() + ":resources"}, cache.invalidations)
}

func TestTouchResourceUpdatedAtUpdatesVersionAndInvalidatesCache(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	updatedAt := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)
	repo := &mockResourceRepo{}
	cache := &mockCacheRepo{}
	svc := newTestResourceService(repo, cache, nil)

	err := svc.TouchResourceUpdatedAt(&models.Resource{ID: resourceID, EventSectionID: &sectionID}, updatedAt)

	require.NoError(t, err)
	assert.Equal(t, 1, repo.touchCalls)
	assert.Equal(t, resourceID, repo.touchedID)
	assert.Equal(t, updatedAt, repo.touchedAt)
	assert.Equal(t, []string{sectionID.String() + ":resources"}, cache.invalidations)
}

func TestDeleteResourceIgnoresCacheInvalidationError(t *testing.T) {
	resourceID := uuid.Must(uuid.NewV4())
	sectionID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resource: &models.Resource{
			ID:             resourceID,
			EventSectionID: &sectionID,
			Path:           "events/missing.webp",
		},
	}
	cache := &mockCacheRepo{invalidateErr: errors.New("redis unavailable")}
	storage := &mockObjectStorage{exists: false}
	svc := newTestResourceService(repo, cache, storage)

	err := svc.DeleteResource(resourceID)

	require.NoError(t, err)
	assert.Equal(t, resourceID, repo.deletedID)
	assert.Empty(t, storage.deletedFilename)
	assert.Equal(t, []string{sectionID.String() + ":resources"}, cache.invalidations)
}

func TestListResourceTypesUsesCatalogCacheKeyAndTTL(t *testing.T) {
	imageTypeID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resourceTypesResponse: []models.ResourceType{
			{ID: imageTypeID, Code: "image", Label: "Imagen"},
		},
	}
	cache := &mockCacheRepo{}
	svc := newTestResourceService(repo, cache, nil)

	types, err := svc.ListResourceTypes()

	require.NoError(t, err)
	require.Len(t, types, 1)
	assert.Equal(t, "image", types[0].Code)
	assert.Equal(t, 1, repo.listResourceTypeCalls)
	assert.Equal(t, "all:"+utils.RedisResourceTypeKey, cache.getKey)
	assert.Equal(t, "all:"+utils.RedisResourceTypeKey, cache.saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisResourceTypeKey], cache.saveTTL)
}

func TestResolveResourceTypeByCodeWorksWithoutCache(t *testing.T) {
	imageTypeID := uuid.Must(uuid.NewV4())
	repo := &mockResourceRepo{
		resourceTypesResponse: []models.ResourceType{
			{ID: imageTypeID, Code: "image", Label: "Imagen"},
		},
	}
	svc := newTestResourceService(repo, nil, nil)

	resolved, err := svc.ResolveResourceTypeByCode("image")

	require.NoError(t, err)
	assert.Equal(t, imageTypeID, resolved)
	assert.Equal(t, 1, repo.listResourceTypeCalls)
}

func TestUploadRawToMomentsFolderUsesMomentImageLimit(t *testing.T) {
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	key, contentType, err := svc.UploadRawToMomentsFolder(
		&memoryMultipartFile{Reader: bytes.NewReader([]byte("image-bytes"))},
		newTestFileHeader("foto.jpg", "image/jpeg", int64(MaxFileSizeBytes+1)),
		"event-1",
	)

	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", contentType)
	assert.Contains(t, key, "moments/event-1/raw/")
	assert.NotEmpty(t, storage.uploadedFilename)
}

func TestUploadRawToMomentsFolderRejectsImagesAboveMomentLimit(t *testing.T) {
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	_, _, err := svc.UploadRawToMomentsFolder(
		&memoryMultipartFile{Reader: bytes.NewReader([]byte("image-bytes"))},
		newTestFileHeader("foto.jpg", "image/jpeg", int64(MaxMomentImageFileSizeBytes+1)),
		"event-1",
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "25 MB")
	assert.Empty(t, storage.uploadedFilename)
}

func TestCompleteMomentMultipartUploadNormalizesPartsBeforeStorage(t *testing.T) {
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	err := svc.CompleteMomentMultipartUpload(
		"moments/event/raw/video.mp4",
		"upload-123",
		[]dtos.CompletedUploadPart{
			{PartNumber: 2, ETag: " etag-2 "},
			{PartNumber: 1, ETag: "etag-1"},
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "moments/event/raw/video.mp4", storage.completedObjectKey)
	assert.Equal(t, "upload-123", storage.completedUploadID)
	assert.Equal(t, []dtos.CompletedUploadPart{
		{PartNumber: 1, ETag: "etag-1"},
		{PartNumber: 2, ETag: "etag-2"},
	}, storage.completedParts)
}

func TestCompleteMomentMultipartUploadRejectsInvalidPartsBeforeStorage(t *testing.T) {
	storage := &mockObjectStorage{}
	svc := newTestResourceService(nil, nil, storage)

	err := svc.CompleteMomentMultipartUpload(
		"moments/event/raw/video.mp4",
		"upload-123",
		[]dtos.CompletedUploadPart{
			{PartNumber: 1, ETag: " "},
			{PartNumber: 2, ETag: "etag-2"},
		},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid multipart parts")
	assert.Empty(t, storage.completedObjectKey)
}

func TestCompleteMomentMultipartUploadTreatsExistingObjectAsCompletedAfterNoSuchUpload(t *testing.T) {
	storage := &mockObjectStorage{exists: true, completeErr: ports.ErrMultipartUploadNotFound}
	svc := newTestResourceService(nil, nil, storage)

	err := svc.CompleteMomentMultipartUpload(
		"moments/event-id/raw/clip.mp4",
		"already-completed",
		[]dtos.CompletedUploadPart{{PartNumber: 1, ETag: "etag-1"}},
	)

	require.NoError(t, err)
	assert.Contains(t, storage.calls, "exists:clip.mp4")
}

func TestCompleteMomentMultipartUploadReturnsNoSuchUploadWhenObjectIsMissing(t *testing.T) {
	storage := &mockObjectStorage{exists: false, completeErr: ports.ErrMultipartUploadNotFound}
	svc := newTestResourceService(nil, nil, storage)

	err := svc.CompleteMomentMultipartUpload(
		"moments/event-id/raw/clip.mp4",
		"missing",
		[]dtos.CompletedUploadPart{{PartNumber: 1, ETag: "etag-1"}},
	)

	assert.ErrorIs(t, err, ports.ErrMultipartUploadNotFound)
}
