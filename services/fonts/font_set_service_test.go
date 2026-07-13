package fonts

import (
	"bytes"
	"context"
	"errors"
	"events-stocks/dtos"
	"testing"
	"time"

	"events-stocks/models"
	"events-stocks/services/ports"
	resourcesService "events-stocks/services/resources"
	"events-stocks/utils"
	"io"
	"mime/multipart"
	"net/http/httptest"

	"github.com/gofrs/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fontRepoMock struct {
	fontSets          []models.FontSet
	createMultipleErr error

	createFontCalled bool
	updateFontCalled bool
	deleteFontCalled bool
}

func (m *fontRepoMock) CreateFont(_ *models.Font) error {
	m.createFontCalled = true
	return nil
}

func (m *fontRepoMock) UpdateFont(_ *models.Font) error {
	m.updateFontCalled = true
	return nil
}

func (m *fontRepoMock) DeleteFont(_ uuid.UUID) error {
	m.deleteFontCalled = true
	return nil
}

func (m *fontRepoMock) GetFontByID(_ uuid.UUID) (*models.Font, error) {
	return nil, nil
}

func (m *fontRepoMock) ListFonts(_ int, _ int, _ string) ([]models.Font, error) {
	return nil, nil
}

func (m *fontRepoMock) CreateMultipleFonts(_ []models.Font) error {
	return m.createMultipleErr
}

func (m *fontRepoMock) CreateFontSet(_ *models.FontSet) error {
	return nil
}

func (m *fontRepoMock) UpdateFontSet(_ *models.FontSet) error {
	return nil
}

func (m *fontRepoMock) DeleteFontSet(_ uuid.UUID) error {
	return nil
}

func (m *fontRepoMock) GetFontSetByID(_ uuid.UUID) (*models.FontSet, error) {
	return nil, nil
}

func (m *fontRepoMock) ListFontSets(_ int, _ int, _ string) ([]models.FontSet, error) {
	return m.fontSets, nil
}

func (m *fontRepoMock) CreateFontPattern(_ *models.FontSetPattern) error {
	return nil
}

func (m *fontRepoMock) UpdateFontPattern(_ *models.FontSetPattern) error {
	return nil
}

func (m *fontRepoMock) DeleteFontPattern(_ uuid.UUID) error {
	return nil
}

func (m *fontRepoMock) GetFontPatternByID(_ uuid.UUID) (*models.FontSetPattern, error) {
	return nil, nil
}

func (m *fontRepoMock) ListFontPatterns(_ *uuid.UUID) ([]models.FontSetPattern, error) {
	return nil, nil
}

var _ ports.FontRepository = (*fontRepoMock)(nil)

type fontCacheRepoMock struct {
	getKeyFunc     func(context.Context, string) (string, error)
	saveKeyFunc    func(context.Context, string, string, time.Duration) error
	invalidateFunc func(string, string) error
}

func (m *fontCacheRepoMock) GetKey(ctx context.Context, key string) (string, error) {
	if m.getKeyFunc != nil {
		return m.getKeyFunc(ctx, key)
	}
	return "", errors.New("cache miss")
}

func (m *fontCacheRepoMock) SaveKey(ctx context.Context, key string, value string, ttl time.Duration) error {
	if m.saveKeyFunc != nil {
		return m.saveKeyFunc(ctx, key, value, ttl)
	}
	return nil
}

func (m *fontCacheRepoMock) Invalidate(resource string, key string) error {
	if m.invalidateFunc != nil {
		return m.invalidateFunc(resource, key)
	}
	return nil
}

func (m *fontCacheRepoMock) DeleteKeysByPattern(context.Context, string) error {
	return nil
}

var _ ports.CacheRepository = (*fontCacheRepoMock)(nil)

func TestFontSetServiceListUsesVersionedCatalogCacheKeyAndTTL(t *testing.T) {
	fontSetID := uuid.Must(uuid.NewV4())
	repo := &fontRepoMock{
		fontSets: []models.FontSet{
			{ID: fontSetID, Name: "Editorial"},
		},
	}
	var getKey string
	var saveKey string
	var savedTTL time.Duration
	cache := &fontCacheRepoMock{
		getKeyFunc: func(_ context.Context, key string) (string, error) {
			getKey = key
			return "", errors.New("cache miss")
		},
		saveKeyFunc: func(_ context.Context, key string, _ string, ttl time.Duration) error {
			saveKey = key
			savedTTL = ttl
			return nil
		},
	}

	fontSets, err := NewFontService(nil, FontServiceDeps{Repo: repo, Cache: cache}).ListFontSets()

	require.NoError(t, err)
	require.Len(t, fontSets, 1)
	assert.Equal(t, "Editorial", fontSets[0].Name)
	assert.Equal(t, "all:"+utils.RedisFontSetKey, getKey)
	assert.Equal(t, "all:"+utils.RedisFontSetKey, saveKey)
	assert.Equal(t, utils.CacheTTLs[utils.RedisFontSetKey], savedTTL)
}

func TestFontServiceInvalidatesFontsAndVersionedFontSetCatalog(t *testing.T) {
	var invalidations []string
	cache := &fontCacheRepoMock{
		invalidateFunc: func(resource string, key string) error {
			invalidations = append(invalidations, key+":"+resource)
			return nil
		},
	}
	service := NewFontService(nil, FontServiceDeps{Repo: &fontRepoMock{}, Cache: cache})

	require.NoError(t, service.CreateFont(&models.Font{Name: "Cormorant"}))

	assert.Equal(t, []string{
		"all:" + utils.RedisFontsKey,
		"all:" + legacyFontSetsCacheResource,
		"all:" + utils.RedisFontSetKey,
	}, invalidations)
}

func TestFontMutationsWorkWithoutCache(t *testing.T) {
	repo := &fontRepoMock{}
	service := NewFontService(nil, FontServiceDeps{Repo: repo})

	require.NoError(t, service.CreateFont(&models.Font{Name: "Cormorant"}))
	require.NoError(t, service.UpdateFont(&models.Font{ID: uuid.Must(uuid.NewV4()), Name: "Cormorant"}))
	require.NoError(t, service.DeleteFont(uuid.Must(uuid.NewV4())))
	assert.True(t, repo.createFontCalled)
	assert.True(t, repo.updateFontCalled)
	assert.True(t, repo.deleteFontCalled)
}

type fontUploadResourceRepo struct {
	deleted []uuid.UUID
}

func (r *fontUploadResourceRepo) CreateResource(resource *models.Resource) error {
	resource.ID = uuid.Must(uuid.NewV4())
	return nil
}
func (r *fontUploadResourceRepo) UpdateResource(*models.Resource) error             { return nil }
func (r *fontUploadResourceRepo) TouchResourceUpdatedAt(uuid.UUID, time.Time) error { return nil }
func (r *fontUploadResourceRepo) DeleteResource(id uuid.UUID) error {
	r.deleted = append(r.deleted, id)
	return nil
}
func (r *fontUploadResourceRepo) GetResourceByID(uuid.UUID) (*models.Resource, error) {
	return nil, errors.New("not found")
}
func (r *fontUploadResourceRepo) ListResourcesBySection(*uuid.UUID) ([]models.Resource, error) {
	return nil, nil
}
func (r *fontUploadResourceRepo) ListResourceTypesRaw() ([]models.ResourceType, error) {
	return []models.ResourceType{{ID: uuid.Must(uuid.NewV4()), Code: "font"}}, nil
}

type fontUploadStorage struct {
	deleted []string
}

func (s *fontUploadStorage) FileExists(string, string, string, string) (bool, string, error) {
	return false, "", nil
}
func (s *fontUploadStorage) GetPresignedFileURL(string, string, string, string, int) (string, error) {
	return "", nil
}
func (s *fontUploadStorage) GetPresignedPutURL(string, string, string, string, int) (string, error) {
	return "", nil
}
func (s *fontUploadStorage) CreateMultipartUpload(string, string, string, string) (string, error) {
	return "", nil
}
func (s *fontUploadStorage) GetPresignedUploadPartURL(string, string, string, string, int, int) (string, error) {
	return "", nil
}
func (s *fontUploadStorage) CompleteMultipartUpload(string, string, string, string, []dtos.CompletedUploadPart) error {
	return nil
}
func (s *fontUploadStorage) AbortMultipartUpload(string, string, string, string) error { return nil }
func (s *fontUploadStorage) UpdateFile([]byte, string, string, string, string, string) (string, error) {
	return "", nil
}
func (s *fontUploadStorage) UploadRawBytesSimple([]byte, string, string, string, string, string) error {
	return nil
}
func (s *fontUploadStorage) DeleteFile(filename, folder, _, _ string) error {
	s.deleted = append(s.deleted, folder+"/"+filename)
	return nil
}
func (s *fontUploadStorage) GetFileStream(string, string, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func fontUploadHeaders(t *testing.T) []*multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "premium.woff2")
	require.NoError(t, err)
	_, err = part.Write([]byte("wOF2-test-font-payload"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest("POST", "/fonts", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(1<<20))
	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })
	return req.MultipartForm.File["files"]
}

func TestUploadAndCreateFontsRollsBackResourcesWhenFontBatchInsertFails(t *testing.T) {
	resourceRepo := &fontUploadResourceRepo{}
	storage := &fontUploadStorage{}
	resourceSvc := resourcesService.NewResourceService(
		&models.Config{AwsBucketName: "events-bucket"},
		resourcesService.ResourceServiceDeps{Repo: resourceRepo, Storage: storage},
	)
	fontRepo := &fontRepoMock{createMultipleErr: errors.New("font insert failed")}
	svc := NewFontService(resourceSvc, FontServiceDeps{Repo: fontRepo})

	fonts, err := svc.UploadAndCreateFonts(fontUploadHeaders(t))

	require.Error(t, err)
	assert.Nil(t, fonts)
	require.Len(t, resourceRepo.deleted, 1)
	require.Len(t, storage.deleted, 1)
	assert.Contains(t, storage.deleted[0], "base/fonts/")
}
