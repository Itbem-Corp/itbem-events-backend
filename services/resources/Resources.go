package services

import (
	"context"
	"errors"
	"events-stocks/configuration/constants"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/awsrepository"
	"events-stocks/services/cacheutil"
	"events-stocks/services/ports"
	services "events-stocks/services/validations"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	MaxFileSizeMB                 = 10 // general resources: images, fonts
	MaxFileSizeBytes              = MaxFileSizeMB * 1024 * 1024
	MaxMomentImageFileSizeMB      = 25 // public moment image uploads
	MaxMomentImageFileSizeBytes   = MaxMomentImageFileSizeMB * 1024 * 1024
	MaxVideoFileSizeMB            = 200 // moment video uploads
	MaxVideoFileSizeBytes         = MaxVideoFileSizeMB * 1024 * 1024
	maxMomentMultipartParts       = MaxVideoFileSizeBytes / (5 * 1024 * 1024)
	maxS3ETagLength               = 128
	ErrPrefixValidate             = "validation_error:"
	ResourceViewURLTTLMinutes     = 720
	ResourceMutationURLTTLMinutes = 60
)

var AllowedMimeTypes = map[string]bool{
	// Imágenes
	"image/jpeg":    true,
	"image/png":     true,
	"image/gif":     true,
	"image/svg+xml": true,
	"image/webp":    true,
	"image/heic":    true,
	"image/heif":    true,

	// Videos
	"video/mp4":        true,
	"video/webm":       true,
	"video/quicktime":  true,
	"video/x-msvideo":  true,
	"video/x-matroska": true,

	// Audios
	"audio/mpeg": true,
	"audio/ogg":  true,
	"audio/wav":  true,
	"audio/aac":  true,
	"audio/flac": true,

	"image/avif":  true,
	"video/x-m4v": true,
	"video/3gpp":  true,

	// Fonts
	"font/ttf":                      true,
	"font/otf":                      true,
	"font/woff":                     true,
	"font/woff2":                    true,
	"application/vnd.ms-fontobject": true,
	"font/sfnt":                     true,
}

var momentUploadAllowedMimeTypes = map[string]bool{
	"image/jpeg":       true,
	"image/png":        true,
	"image/gif":        true,
	"image/webp":       true,
	"image/heic":       true,
	"image/heif":       true,
	"image/avif":       true,
	"video/mp4":        true,
	"video/webm":       true,
	"video/quicktime":  true,
	"video/x-m4v":      true,
	"video/3gpp":       true,
	"video/x-msvideo":  true,
	"video/x-matroska": true,
}

type ResourceService struct {
	Bucket     string
	Provider   string
	UploadPath string // e.g. "resources"
	ObjectRoot string // e.g. "organizations/{clientID}"
	Optimizer  *ImageOptimizerService
	repo       ports.ResourceRepository
	cache      ports.CacheRepository
	storage    ports.ObjectStorageRepository
}

type ResourceServiceDeps struct {
	Repo    ports.ResourceRepository
	Cache   ports.CacheRepository
	Storage ports.ObjectStorageRepository
}

type resourceSectionVersionedDeleter interface {
	DeleteResourceAndTouchSection(resourceID uuid.UUID, sectionID uuid.UUID, updatedAt time.Time) error
}

var _resourceSvc *ResourceService

func SetDefaultResourceService(svc *ResourceService) {
	_resourceSvc = svc
}

func NewResourceService(c *models.Config, deps ...ResourceServiceDeps) *ResourceService {
	var dep ResourceServiceDeps
	if len(deps) > 0 {
		dep = deps[0]
	}
	return &ResourceService{
		Bucket:     c.AwsBucketName,
		Provider:   constants.DefaultCloudProvider,
		UploadPath: constants.EventsBucketFolder,
		Optimizer:  NewImageOptimizerService(),
		repo:       dep.Repo,
		cache:      dep.Cache,
		storage:    dep.Storage,
	}
}

// WithBucket creates an immutable request/entity-scoped view. It is safe to
// use concurrently and avoids mutating the process-wide default service.
func (rs *ResourceService) WithBucket(bucket string) *ResourceService {
	if rs == nil || strings.TrimSpace(bucket) == "" || strings.TrimSpace(bucket) == rs.Bucket {
		return rs
	}
	clone := *rs
	clone.Bucket = strings.TrimSpace(bucket)
	return &clone
}

// WithOrganization creates an immutable storage-scoped view. Buckets isolate
// applications; this prefix isolates organizations inside each application.
func (rs *ResourceService) WithOrganization(clientID *uuid.UUID) *ResourceService {
	if rs == nil || clientID == nil || *clientID == uuid.Nil {
		return rs
	}
	root := fmt.Sprintf("organizations/%s", clientID.String())
	if rs.ObjectRoot == root {
		return rs
	}
	clone := *rs
	clone.ObjectRoot = root
	return &clone
}

func (rs *ResourceService) scopedObjectPath(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	root := strings.Trim(strings.TrimSpace(rs.ObjectRoot), "/")
	if root == "" || path == "" || path == root || strings.HasPrefix(path, root+"/") {
		return path
	}
	return root + "/" + path
}

// CanonicalObjectKey normalizes legacy S3/CDN URLs at the storage boundary.
// HTTP handlers and domain services can therefore operate only on object keys.
func (rs *ResourceService) CanonicalObjectKey(raw string) string {
	return awsrepository.S3KeyFromURL(raw, rs.Bucket)
}

func resourceServiceUnavailable() error {
	return fmt.Errorf("resource service not initialized")
}

func (rs *ResourceService) requireRepo() (ports.ResourceRepository, error) {
	if rs == nil || rs.repo == nil {
		return nil, fmt.Errorf("resource repository not configured")
	}
	return rs.repo, nil
}

func (rs *ResourceService) requireStorage() (ports.ObjectStorageRepository, error) {
	if rs == nil || rs.storage == nil {
		return nil, fmt.Errorf("object storage repository not configured")
	}
	return rs.storage, nil
}

func (rs *ResourceService) GetResourceByID(id uuid.UUID) (*models.Resource, string, error) {
	resource, err := rs.GetResourceRecordByID(id)
	if err != nil {
		return nil, "", err
	}

	trimmedPath := strings.TrimSpace(resource.Path)
	if trimmedPath == "" {
		return nil, "", fmt.Errorf("resource has no path assigned")
	}
	if utils.IsAbsoluteURLLike(trimmedPath) {
		return resource, trimmedPath, nil
	}

	storage, err := rs.requireStorage()
	if err != nil {
		return nil, "", err
	}

	// Asegúrate de que resource.Path exista
	folder, filename := resourceStoragePathParts(trimmedPath, rs.UploadPath)
	if filename == "" {
		return nil, "", fmt.Errorf("resource has no path assigned")
	}

	exists, _, err := storage.FileExists(filename, folder, rs.Bucket, rs.Provider)
	if err != nil {
		return nil, "", fmt.Errorf("failed to verify file existence: %w", err)
	}
	if !exists {
		return nil, "", fmt.Errorf("file associated with resource not found in bucket")
	}

	viewURL, err := storage.GetPresignedFileURL(filename, folder, rs.WithBucket(resource.MediaBucket).Bucket, rs.Provider, ResourceViewURLTTLMinutes)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return resource, viewURL, nil
}

func (rs *ResourceService) GetResourceRecordByID(id uuid.UUID) (*models.Resource, error) {
	repo, err := rs.requireRepo()
	if err != nil {
		return nil, err
	}

	resource, err := repo.GetResourceByID(id)
	if err != nil {
		return nil, fmt.Errorf("resource not found: %w", err)
	}
	return resource, nil
}

func (rs *ResourceService) GetResourcesBySectionID(sectionID uuid.UUID) ([]dtos.ResourceResponse, error) {
	return cacheutil.GetOrLoadJSON(
		context.Background(),
		rs.cache,
		rs.sectionResourcesCacheKey(sectionID),
		utils.CacheTTLs[utils.RedisResourcesKey],
		func() ([]dtos.ResourceResponse, error) {
			return rs.loadResourcesBySectionID(sectionID)
		},
	)
}

func (rs *ResourceService) GetAdminResourcesBySectionID(sectionID uuid.UUID) ([]dtos.AdminResourceResponse, error) {
	return rs.loadAdminResourcesBySectionID(sectionID)
}

func (rs *ResourceService) sectionResourcesCacheKey(sectionID uuid.UUID) string {
	return sectionID.String() + ":" + utils.RedisResourcesKey
}

func (rs *ResourceService) loadResourcesBySectionID(sectionID uuid.UUID) ([]dtos.ResourceResponse, error) {
	resources, storage, err := rs.listResourcesBySectionWithStorage(sectionID)
	if err != nil {
		return nil, err
	}

	result := dtos.NewResourceResponses(resources, func(resource models.Resource) (string, *time.Time, bool) {
		return rs.resourceViewURLFor(storage, resource, ResourceViewURLTTLMinutes)
	})
	return result, nil
}

func (rs *ResourceService) loadAdminResourcesBySectionID(sectionID uuid.UUID) ([]dtos.AdminResourceResponse, error) {
	resources, storage, err := rs.listResourcesBySectionWithStorage(sectionID)
	if err != nil {
		return nil, err
	}

	result := dtos.NewAdminResourceResponses(resources, func(resource models.Resource) (string, *time.Time, bool) {
		return rs.resourceViewURLFor(storage, resource, ResourceViewURLTTLMinutes)
	})
	return result, nil
}

func (rs *ResourceService) listResourcesBySectionWithStorage(sectionID uuid.UUID) ([]models.Resource, ports.ObjectStorageRepository, error) {
	repo, err := rs.requireRepo()
	if err != nil {
		return nil, nil, err
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return nil, nil, err
	}

	resources, err := repo.ListResourcesBySection(&sectionID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list resources: %w", err)
	}
	sortResourcesByRenderOrder(resources)

	return resources, storage, nil
}

func sortResourcesByRenderOrder(resources []models.Resource) {
	sort.SliceStable(resources, func(i, j int) bool {
		leftPosition := resourcePositionValue(resources[i])
		rightPosition := resourcePositionValue(resources[j])
		if leftPosition != rightPosition {
			return leftPosition < rightPosition
		}
		return resources[i].ID.String() < resources[j].ID.String()
	})
}

func resourcePositionValue(resource models.Resource) int {
	if resource.Position == nil {
		return 0
	}
	return *resource.Position
}

func (rs *ResourceService) resourceViewURLFor(storage ports.ObjectStorageRepository, resource models.Resource, ttlMinutes int) (string, *time.Time, bool) {
	trimmedPath := strings.TrimSpace(resource.Path)
	if trimmedPath == "" {
		return "", nil, false
	}
	if utils.IsAbsoluteURLLike(trimmedPath) {
		return trimmedPath, nil, true
	}

	folder, filename := resourceStoragePathParts(trimmedPath, rs.UploadPath)
	if filename == "" {
		return "", nil, false
	}

	viewURL, err := storage.GetPresignedFileURL(filename, folder, rs.WithBucket(resource.MediaBucket).Bucket, rs.Provider, ttlMinutes)
	if err != nil {
		return "", nil, false
	}

	expiresAt := resourceViewURLExpiresAt(ttlMinutes)
	return viewURL, &expiresAt, true
}

func resourceViewURLExpiresAt(ttlMinutes int) time.Time {
	return time.Now().UTC().Add(time.Duration(ttlMinutes) * time.Minute)
}

func resourceStoragePathParts(path string, defaultFolder string) (string, string) {
	cleanPath := strings.Trim(strings.TrimSpace(path), "/")
	cleanDefaultFolder := strings.Trim(strings.TrimSpace(defaultFolder), "/")
	if cleanPath == "" {
		return cleanDefaultFolder, ""
	}
	parts := strings.Split(cleanPath, "/")
	if len(parts) == 1 {
		return cleanDefaultFolder, parts[0]
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1]
}

func (rs *ResourceService) FileExists(path string) (bool, string, error) {
	storage, err := rs.requireStorage()
	if err != nil {
		return false, "", err
	}
	folder, filename := resourceStoragePathParts(path, rs.UploadPath)
	return storage.FileExists(filename, folder, rs.Bucket, rs.Provider)
}

func (rs *ResourceService) DeleteFileIfExists(path string) error {
	storage, err := rs.requireStorage()
	if err != nil {
		return err
	}
	folder, filename := resourceStoragePathParts(path, rs.UploadPath)
	if filename == "" {
		return nil
	}
	exists, _, err := storage.FileExists(filename, folder, rs.Bucket, rs.Provider)
	if err != nil {
		return fmt.Errorf("error checking file: %w", err)
	}
	if !exists {
		return nil
	}

	if err := storage.DeleteFile(filename, folder, rs.Bucket, rs.Provider); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	return nil
}

func (rs *ResourceService) DeleteResource(id uuid.UUID) error {
	repo, err := rs.requireRepo()
	if err != nil {
		return err
	}

	// 🔍 Obtener el recurso desde DB
	resource, err := repo.GetResourceByID(id)
	if err != nil {
		return fmt.Errorf("resource not found: %w", err)
	}

	// 🗑️ Eliminar registro en DB
	if err := rs.deleteResourceRecord(id, resource.EventSectionID); err != nil {
		return fmt.Errorf("failed to delete resource from DB: %w", err)
	}
	_ = rs.WithBucket(resource.MediaBucket).DeleteFileIfExists(resource.Path)

	return nil
}

func (rs *ResourceService) UpdateResource(resource *models.Resource) error {
	repo, err := rs.requireRepo()
	if err != nil {
		return err
	}

	if err := repo.UpdateResource(resource); err != nil {
		return err
	}

	return rs.InvalidateSectionResourceCache(resource.EventSectionID)
}

func (rs *ResourceService) TouchResourceUpdatedAt(resource *models.Resource, updatedAt time.Time) error {
	if resource == nil || resource.ID == uuid.Nil {
		return fmt.Errorf("resource missing")
	}

	repo, err := rs.requireRepo()
	if err != nil {
		return err
	}

	if err := repo.TouchResourceUpdatedAt(resource.ID, updatedAt); err != nil {
		return err
	}

	return rs.InvalidateSectionResourceCache(resource.EventSectionID)
}

func (rs *ResourceService) InvalidateSectionResourceCache(sectionID *uuid.UUID) error {
	if rs == nil || sectionID == nil || rs.cache == nil {
		return nil
	}
	_ = rs.cache.Invalidate("resources", sectionID.String())
	return nil
}

func (rs *ResourceService) UpdateFileContent(
	file multipart.File,
	path string,
	header *multipart.FileHeader,
) (string, error) {
	folder, filename := resourceStoragePathParts(path, rs.UploadPath)
	if filename == "" {
		return "", fmt.Errorf("resource path missing")
	}
	optimized, newFilename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, filename)
	if err != nil {
		return "", err
	}

	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	_, err = storage.UpdateFile(optimized, newFilename, contentType, folder, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to update file: %w", err)
	}

	return fmt.Sprintf("%s/%s", folder, newFilename), nil
}

func (rs *ResourceService) ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error) {
	repo, err := rs.requireRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListResourcesBySection(sectionID)
}

func (rs *ResourceService) ReplaceFile(
	oldPath string,
	file multipart.File,
	header *multipart.FileHeader,
) (string, error) {
	folder, _ := resourceStoragePathParts(oldPath, rs.UploadPath)
	optimized, newFilename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return "", err
	}

	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	err = storage.UploadRawBytesSimple(optimized, newFilename, contentType, folder, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to upload replacement: %w", err)
	}

	return fmt.Sprintf("%s/%s", folder, newFilename), nil
}

func (rs *ResourceService) UploadAndCreateResource(
	file multipart.File,
	header *multipart.FileHeader,
	sectionID *uuid.UUID,
	resourceTypeID uuid.UUID,
	altText, title string,
	requestedPosition *int,
) (*models.Resource, error) {
	repo, err := rs.requireRepo()
	if err != nil {
		return nil, err
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return nil, err
	}

	position := 0
	if requestedPosition != nil {
		position = *requestedPosition
	} else {
		existing, err := repo.ListResourcesBySection(sectionID)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing resources: %w", err)
		}

		for _, r := range existing {
			if r.Position != nil && *r.Position >= position {
				position = *r.Position + 1
			}
		}
	}

	optimized, filename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return nil, err
	}

	uploadPath := rs.scopedObjectPath(rs.UploadPath)
	err = storage.UploadRawBytesSimple(optimized, filename, contentType, uploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to upload resource: %w", err)
	}

	resource := &models.Resource{
		EventSectionID: sectionID,
		ResourceTypeID: resourceTypeID,
		Path:           fmt.Sprintf("%s/%s", uploadPath, filename),
		MediaBucket:    rs.Bucket,
		AltText:        altText,
		Title:          title,
		Position:       utils.PtrInt(position),
	}

	if err := rs.CreateResource(resource); err != nil {
		_ = storage.DeleteFile(filename, uploadPath, rs.Bucket, rs.Provider)
		return nil, fmt.Errorf("failed to create resource in DB: %w", err)
	}

	return resource, nil
}

// UploadEventCover uploads and optimizes a cover image to the events/ S3 folder.
// Returns the S3 key ("events/{uuid}.webp") to store in Event.CoverImageURL.
func (rs *ResourceService) UploadEventCover(
	file multipart.File,
	header *multipart.FileHeader,
) (string, models.MediaVariants, error) {
	optimized, filename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return "", nil, err
	}
	if err := requireImageUploadContentType(contentType); err != nil {
		return "", nil, err
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return "", nil, err
	}
	uploadPath := rs.scopedObjectPath(rs.UploadPath)
	err = storage.UploadRawBytesSimple(optimized, filename, contentType, uploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return "", nil, fmt.Errorf("failed to upload cover image: %w", err)
	}
	mainPath := fmt.Sprintf("%s/%s", uploadPath, filename)
	sourceWidth, widthErr := rs.Optimizer.ImageWidth(optimized)
	if widthErr != nil {
		_ = rs.DeleteObjectByPath(mainPath)
		return "", nil, widthErr
	}
	widths := responsiveCoverWidths(sourceWidth)
	generated, variantErr := rs.Optimizer.ResponsiveWebPVariants(optimized, widths, 82)
	if variantErr != nil {
		_ = rs.DeleteObjectByPath(mainPath)
		return "", nil, variantErr
	}
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	variants := make(models.MediaVariants, 0, len(generated))
	for _, variant := range generated {
		variantFilename := fmt.Sprintf("%s-%d.webp", stem, variant.Width)
		if uploadErr := storage.UploadRawBytesSimple(variant.Bytes, variantFilename, "image/webp", uploadPath, rs.Bucket, rs.Provider); uploadErr != nil {
			_ = rs.DeleteObjectByPath(mainPath)
			for _, uploaded := range variants {
				_ = rs.DeleteObjectByPath(uploaded.ObjectKey)
			}
			return "", nil, fmt.Errorf("failed to upload cover variant: %w", uploadErr)
		}
		variants = append(variants, models.MediaVariant{
			ObjectKey: fmt.Sprintf("%s/%s", uploadPath, variantFilename),
			Width:     variant.Width, Format: "webp", Bytes: int64(len(variant.Bytes)),
		})
	}
	return mainPath, variants, nil
}

// UploadRawEventCover performs only bounded validation and storage I/O. CPU
// intensive decode/resize work is delegated to the image queue so API latency
// is independent of source dimensions and image format.
func (rs *ResourceService) UploadRawEventCover(file multipart.File, header *multipart.FileHeader, eventID string) (string, string, int64, error) {
	contentType, err := detectUploadContentType(file, header)
	if err != nil {
		return "", "", 0, err
	}
	if err := requireImageUploadContentType(contentType); err != nil {
		return "", "", 0, err
	}
	if header.Size > MaxFileSizeBytes {
		return "", "", 0, services.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", MaxFileSizeMB)}
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileSizeBytes+1))
	if err != nil {
		return "", "", 0, fmt.Errorf("read cover upload: %w", err)
	}
	if len(data) == 0 {
		return "", "", 0, services.ValidationError{Msg: "uploaded file is empty"}
	}
	if len(data) > MaxFileSizeBytes {
		return "", "", 0, services.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", MaxFileSizeMB)}
	}
	u, _ := uuid.NewV4()
	filename := updateFilenameExtension(u.String(), contentType)
	folder := rs.scopedObjectPath(fmt.Sprintf("%s/%s/raw", strings.Trim(rs.UploadPath, "/"), eventID))
	storage, err := rs.requireStorage()
	if err != nil {
		return "", "", 0, err
	}
	if err := storage.UploadRawBytesSimple(data, filename, contentType, folder, rs.Bucket, rs.Provider); err != nil {
		return "", "", 0, fmt.Errorf("failed to upload raw cover image: %w", err)
	}
	return fmt.Sprintf("%s/%s", folder, filename), contentType, int64(len(data)), nil
}

func responsiveCoverWidths(sourceWidth int) []int {
	if sourceWidth <= 640 {
		return nil
	}
	widths := make([]int, 0, 3)
	for _, width := range []int{640, 1280, 1920} {
		if width < sourceWidth {
			widths = append(widths, width)
		}
	}
	if sourceWidth <= 1920 {
		widths = append(widths, sourceWidth)
	}
	return widths
}

// UploadRawToMomentsFolder uploads a guest moment file to S3 WITHOUT optimization.
// The raw file is stored under moments/{eventID}/raw/ for async Lambda processing.
// Returns the S3 key, detected content-type, and any error.
// Does NOT create a Resource record — the path is stored in Moment.ContentURL.
func (rs *ResourceService) UploadRawToMomentsFolder(
	file multipart.File,
	header *multipart.FileHeader,
	eventID string,
) (s3Key string, contentType string, err error) {
	return rs.UploadRawToMomentsFolderContext(context.Background(), file, header, eventID)
}

// UploadRawToMomentsFolderContext is the request-aware variant used by HTTP
// handlers so a disconnected client cancels the remaining S3 transfer.
func (rs *ResourceService) UploadRawToMomentsFolderContext(
	ctx context.Context,
	file multipart.File,
	header *multipart.FileHeader,
	eventID string,
) (s3Key string, contentType string, err error) {
	ct, err := normalizeMomentUploadContentType(header.Filename, header.Header.Get("Content-Type"))
	if err != nil {
		return "", "", err
	}
	isVid := strings.HasPrefix(ct, "video/")

	// Enforce per-type byte limits before any upload. The browser also rejects
	// oversized/overlong videos for immediate UX feedback, while the media
	// processor remains authoritative: it uses ffprobe to enforce duration,
	// dimensions, and the same 200 MiB input ceiling before transcoding.
	maxBytes := int64(MaxMomentImageFileSizeBytes) // 25 MB for images
	if isVid {
		maxBytes = int64(MaxVideoFileSizeBytes) // 200 MB for videos
	}
	if header.Size > maxBytes {
		limitMB := MaxMomentImageFileSizeMB
		if isVid {
			limitMB = MaxVideoFileSizeMB
		}
		return "", "", services.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", limitMB)}
	}

	// Build a UUID-based filename preserving extension
	u, _ := uuid.NewV4()
	ext := ""
	if idx := strings.LastIndex(header.Filename, "."); idx != -1 {
		ext = strings.ToLower(header.Filename[idx:])
	}
	filename := u.String() + ext

	// Organize: moments/{eventID}/raw/{uuid}.ext
	folder := rs.scopedObjectPath(fmt.Sprintf("moments/%s/raw", eventID))
	storage, err := rs.requireStorage()
	if err != nil {
		return "", "", err
	}
	var uploadErr error
	if streamUploader, ok := storage.(ports.ObjectStorageStreamUploader); ok {
		uploadErr = streamUploader.UploadStream(ctx, io.LimitReader(file, maxBytes+1), header.Size, filename, ct, folder, rs.Bucket, rs.Provider)
	} else {
		// Compatibility path for lightweight and third-party storage adapters.
		raw, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
		if readErr != nil {
			return "", "", fmt.Errorf("failed to read uploaded file: %w", readErr)
		}
		if int64(len(raw)) > maxBytes {
			return "", "", services.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", maxBytes/(1024*1024))}
		}
		uploadErr = storage.UploadRawBytesSimple(raw, filename, ct, folder, rs.Bucket, rs.Provider)
	}
	if uploadErr != nil {
		return "", "", fmt.Errorf("failed to upload raw moment: %w", uploadErr)
	}

	return fmt.Sprintf("%s/%s", folder, filename), ct, nil
}

func (rs *ResourceService) PrepareMomentUploadURL(eventID, filename, contentType string) (s3Key, uploadURL, normalizedContentType string, err error) {
	ct, err := normalizeMomentUploadContentType(filename, contentType)
	if err != nil {
		return "", "", "", err
	}
	key := rs.buildMomentRawKey(eventID, filename)
	storage, err := rs.requireStorage()
	if err != nil {
		return "", "", "", err
	}
	url, err := storage.GetPresignedPutURL(key, rs.Bucket, rs.Provider, ct, 15)
	if err != nil {
		return "", "", "", err
	}
	return key, url, ct, nil
}

func (rs *ResourceService) PrepareMomentMultipartUpload(eventID, filename, contentType string, partCount int) (s3Key, uploadID string, partURLs []dtos.PresignedUploadPart, normalizedContentType string, err error) {
	if partCount <= 0 {
		return "", "", nil, "", services.ValidationError{Msg: "part_count must be greater than zero"}
	}
	if partCount > 10000 {
		return "", "", nil, "", services.ValidationError{Msg: "too many multipart parts"}
	}
	ct, err := normalizeMomentUploadContentType(filename, contentType)
	if err != nil {
		return "", "", nil, "", err
	}
	key := rs.buildMomentRawKey(eventID, filename)
	storage, err := rs.requireStorage()
	if err != nil {
		return "", "", nil, "", err
	}
	uploadID, err = storage.CreateMultipartUpload(key, rs.Bucket, rs.Provider, ct)
	if err != nil {
		return "", "", nil, "", err
	}
	partURLs = make([]dtos.PresignedUploadPart, 0, partCount)
	for partNumber := 1; partNumber <= partCount; partNumber++ {
		url, partErr := storage.GetPresignedUploadPartURL(key, rs.Bucket, rs.Provider, uploadID, partNumber, 60)
		if partErr != nil {
			_ = storage.AbortMultipartUpload(key, rs.Bucket, rs.Provider, uploadID)
			return "", "", nil, "", partErr
		}
		partURLs = append(partURLs, dtos.PresignedUploadPart{
			PartNumber: partNumber,
			URL:        url,
		})
	}
	return key, uploadID, partURLs, ct, nil
}

func (rs *ResourceService) CompleteMomentMultipartUpload(s3Key, uploadID string, parts []dtos.CompletedUploadPart) error {
	normalizedParts := dtos.NormalizeCompletedUploadParts(parts)
	if len(normalizedParts) == 0 {
		return services.ValidationError{Msg: "parts are required"}
	}
	if len(normalizedParts) != len(parts) {
		return services.ValidationError{Msg: "invalid multipart parts"}
	}
	if len(normalizedParts) > maxMomentMultipartParts {
		return services.ValidationError{Msg: "too many multipart parts"}
	}
	for index, part := range normalizedParts {
		if part.PartNumber != index+1 || len(part.ETag) > maxS3ETagLength || strings.IndexFunc(part.ETag, unicode.IsControl) >= 0 {
			return services.ValidationError{Msg: "invalid multipart parts"}
		}
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return err
	}
	err = storage.CompleteMultipartUpload(s3Key, rs.Bucket, rs.Provider, uploadID, normalizedParts)
	if !errors.Is(err, ports.ErrMultipartUploadNotFound) {
		return err
	}

	// S3 removes an upload ID after a successful CompleteMultipartUpload. If
	// the response was lost, a safe client retry receives NoSuchUpload even
	// though the completed object already exists. Treat only that state as a
	// successful idempotent completion.
	folder, filename := resourceStoragePathParts(s3Key, "moments")
	exists, _, existsErr := storage.FileExists(filename, folder, rs.Bucket, rs.Provider)
	if existsErr != nil {
		return fmt.Errorf("verify previously completed multipart object: %w", existsErr)
	}
	if exists {
		return nil
	}
	return err
}

func (rs *ResourceService) AbortMomentMultipartUpload(s3Key, uploadID string) error {
	storage, err := rs.requireStorage()
	if err != nil {
		return err
	}
	return storage.AbortMultipartUpload(s3Key, rs.Bucket, rs.Provider, uploadID)
}

func (rs *ResourceService) ValidateMomentRawKey(eventID, s3Key string) error {
	expectedPrefix := rs.scopedObjectPath(fmt.Sprintf("moments/%s/raw", eventID)) + "/"
	if !strings.HasPrefix(s3Key, expectedPrefix) {
		return services.ValidationError{Msg: "invalid upload key for event"}
	}
	if strings.Contains(s3Key, "..") || strings.Contains(s3Key, "\\") {
		return services.ValidationError{Msg: "invalid upload key"}
	}
	return nil
}

// VerifyMomentUpload confirms that a presigned upload reached storage before a
// Moment record is created. Real storage implementations additionally expose
// object size and Content-Type; lightweight test adapters fall back to exists.
func (rs *ResourceService) VerifyMomentUpload(s3Key, requestedContentType string) (string, error) {
	return rs.VerifyMomentUploadSize(s3Key, requestedContentType, 0)
}

func (rs *ResourceService) VerifyMomentUploadSize(s3Key, requestedContentType string, expectedSize int64) (string, error) {
	contentType, err := normalizeMomentUploadContentType(s3Key, requestedContentType)
	if err != nil {
		return "", err
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	folder, filename := resourceStoragePathParts(s3Key, "moments")
	if filename == "" {
		return "", services.ValidationError{Msg: "invalid upload key"}
	}

	if metadataReader, ok := storage.(ports.ObjectStorageMetadataReader); ok {
		metadata, metadataErr := metadataReader.GetObjectMetadata(filename, folder, rs.Bucket, rs.Provider)
		if metadataErr != nil {
			return "", fmt.Errorf("failed to verify uploaded object: %w", metadataErr)
		}
		if metadata.Size <= 0 {
			return "", services.ValidationError{Msg: "uploaded file is empty"}
		}
		if expectedSize > 0 && metadata.Size != expectedSize {
			return "", services.ValidationError{Msg: "uploaded file size does not match the upload request"}
		}
		storedType := canonicalUploadContentType(metadata.ContentType)
		if storedType != "" && storedType != "application/octet-stream" {
			storedType, err = normalizeMomentUploadContentType(s3Key, storedType)
			if err != nil {
				return "", err
			}
			if storedType != contentType {
				return "", services.ValidationError{Msg: "uploaded file content type does not match the upload request"}
			}
		}
		maxBytes, maxMB := momentUploadLimit(contentType)
		if metadata.Size > maxBytes {
			return "", services.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", maxMB)}
		}
		return contentType, nil
	}

	exists, _, existsErr := storage.FileExists(filename, folder, rs.Bucket, rs.Provider)
	if existsErr != nil {
		return "", fmt.Errorf("failed to verify uploaded object: %w", existsErr)
	}
	if !exists {
		return "", services.ValidationError{Msg: "uploaded file was not found; wait for the upload to finish and retry"}
	}
	return contentType, nil
}

// DeleteMomentUpload removes a raw moment object without issuing a separate
// HEAD request. S3 DeleteObject is idempotent, which makes this suitable for
// compensating failed confirmation requests and retry races.
func (rs *ResourceService) DeleteMomentUpload(s3Key string) error {
	storage, err := rs.requireStorage()
	if err != nil {
		return err
	}
	folder, filename := resourceStoragePathParts(s3Key, "moments")
	if filename == "" {
		return services.ValidationError{Msg: "invalid upload key"}
	}
	return storage.DeleteFile(filename, folder, rs.Bucket, rs.Provider)
}

func (rs *ResourceService) MarkMomentUploadConfirmed(ctx context.Context, s3Key string) error {
	storage, err := rs.requireStorage()
	if err != nil {
		return err
	}
	confirmer, ok := storage.(ports.ObjectStorageUploadConfirmer)
	if !ok {
		return nil
	}
	folder, filename := resourceStoragePathParts(s3Key, "moments")
	if filename == "" {
		return services.ValidationError{Msg: "invalid upload key"}
	}
	return confirmer.MarkUploadConfirmed(ctx, filename, folder, rs.Bucket, rs.Provider)
}

func momentUploadLimit(contentType string) (int64, int) {
	if strings.HasPrefix(contentType, "video/") {
		return int64(MaxVideoFileSizeBytes), MaxVideoFileSizeMB
	}
	return int64(MaxMomentImageFileSizeBytes), MaxMomentImageFileSizeMB
}

func (rs *ResourceService) buildMomentRawKey(eventID, filename string) string {
	u, _ := uuid.NewV4()
	ext := ""
	if idx := strings.LastIndex(filename, "."); idx != -1 {
		ext = strings.ToLower(filename[idx:])
	}
	return rs.scopedObjectPath(fmt.Sprintf("moments/%s/raw/%s%s", eventID, u.String(), ext))
}

func normalizeMomentUploadContentType(filename, contentType string) (string, error) {
	ct := canonicalUploadContentType(contentType)
	if ct == "" || ct == "application/octet-stream" {
		ext := ""
		if idx := strings.LastIndex(filename, "."); idx != -1 {
			ext = strings.ToLower(filename[idx+1:])
		}
		ct = canonicalUploadContentType(guessMimeType(ext))
	}
	if !momentUploadAllowedMimeTypes[ct] {
		return "", services.ValidationError{Msg: fmt.Sprintf("unsupported file type for moments: %s", ct)}
	}
	return ct, nil
}

func canonicalUploadContentType(contentType string) string {
	ct := strings.TrimSpace(strings.ToLower(contentType))
	if ct == "image/jpg" {
		return "image/jpeg"
	}
	return ct
}

// UploadToMomentsFolder is kept for backward compatibility.
// Deprecated: use UploadRawToMomentsFolder.
func (rs *ResourceService) UploadToMomentsFolder(
	file multipart.File,
	header *multipart.FileHeader,
) (string, error) {
	optimized, filename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return "", err
	}

	momentsPath := "moments"
	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	err = storage.UploadRawBytesSimple(optimized, filename, contentType, momentsPath, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to upload moment: %w", err)
	}

	return fmt.Sprintf("%s/%s", momentsPath, filename), nil
}

func (rs *ResourceService) UploadMultipleResources(
	files []*multipart.FileHeader,
	sectionID *uuid.UUID,
	resourceTypeID uuid.UUID,
) ([]*models.Resource, error) {
	var uploaded []*models.Resource
	storage, err := rs.requireStorage()
	if err != nil {
		return nil, err
	}
	created := make([]*models.Resource, 0, len(files))
	uploadedNames := make([]string, 0, len(files))
	uploadPath := rs.scopedObjectPath(rs.UploadPath)
	cleanup := func() { rs.cleanupBatchUploads(storage, created, uploadedNames, uploadPath) }

	for i, header := range files {
		file, err := header.Open()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("error opening file %d: %w", i+1, err)
		}

		sectionPart := "unscoped"
		if sectionID != nil {
			sectionPart = sectionID.String()
		}
		forcedFilename := fmt.Sprintf("resource-%s-%d%s", sectionPart, i+1, header.Filename)

		content, finalName, finalType, err := rs.sanitizeAndOptimizeUpload(file, header, forcedFilename)
		file.Close()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to process file %d: %w", i+1, err)
		}
		err = storage.UploadRawBytesSimple(content, finalName, finalType, uploadPath, rs.Bucket, rs.Provider)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("upload failed for file %d: %w", i+1, err)
		}
		uploadedNames = append(uploadedNames, finalName)

		resource := &models.Resource{
			EventSectionID: sectionID,
			ResourceTypeID: resourceTypeID,
			Path:           fmt.Sprintf("%s/%s", uploadPath, finalName),
			MediaBucket:    rs.Bucket,
			AltText:        "",
			Title:          finalName,
			Position:       utils.PtrInt(i),
		}

		if err := rs.CreateResource(resource); err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to save resource for file %d: %w", i+1, err)
		}

		created = append(created, resource)
		uploaded = append(uploaded, resource)
	}

	return uploaded, nil
}

func (rs *ResourceService) UploadBaseResources(
	files []*multipart.FileHeader,
	subfolder string,
	resourceTypeName string,
) ([]*models.Resource, error) {
	// 1. Obtener resourceTypeID por nombre
	resourceTypes, err := rs.ListResourceTypes()
	if err != nil {
		return nil, fmt.Errorf("failed to load resource types: %w", err)
	}

	var resourceTypeID uuid.UUID
	for _, rt := range resourceTypes {
		if strings.EqualFold(rt.Code, resourceTypeName) {
			resourceTypeID = rt.ID
			break
		}
	}
	if resourceTypeID == uuid.Nil {
		return nil, fmt.Errorf("resource type '%s' not found", resourceTypeName)
	}

	// 2. Iniciar uploads
	var uploaded []*models.Resource
	storage, err := rs.requireStorage()
	if err != nil {
		return nil, err
	}
	created := make([]*models.Resource, 0, len(files))
	uploadedNames := make([]string, 0, len(files))
	cleanup := func() { rs.cleanupBatchUploads(storage, created, uploadedNames, subfolder) }
	for i, header := range files {
		file, err := header.Open()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("error opening file %d: %w", i+1, err)
		}

		// Nombre forzado si se requiere
		forcedFilename := fmt.Sprintf("base-%s-%d%s", resourceTypeName, i+1, header.Filename)

		// 3. Sanitizar y optimizar
		content, finalName, finalType, err := rs.sanitizeAndOptimizeUpload(file, header, forcedFilename)
		file.Close()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to process file %d: %w", i+1, err)
		}
		if strings.EqualFold(resourceTypeName, "font") && !isFontUploadContentType(finalType) {
			cleanup()
			return nil, fmt.Errorf("failed to process file %d: %w", i+1, services.ValidationError{Msg: fmt.Sprintf("unsupported font type: %s", finalType)})
		}

		// 4. Upload en subfolder
		finalPath := fmt.Sprintf("%s/%s", subfolder, finalName)
		err = storage.UploadRawBytesSimple(content, finalName, finalType, subfolder, rs.Bucket, rs.Provider)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("upload failed for file %d: %w", i+1, err)
		}
		uploadedNames = append(uploadedNames, finalName)

		// 5. Crear modelo sin section
		resource := &models.Resource{
			ResourceTypeID: resourceTypeID,
			Path:           finalPath,
			MediaBucket:    rs.Bucket,
			AltText:        "",
			Title:          finalName,
		}

		if err := rs.CreateResource(resource); err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to save resource for file %d: %w", i+1, err)
		}

		created = append(created, resource)
		uploaded = append(uploaded, resource)
	}

	return uploaded, nil
}

func (rs *ResourceService) cleanupBatchUploads(storage ports.ObjectStorageRepository, created []*models.Resource, uploadedNames []string, folder string) {
	for i := len(created) - 1; i >= 0; i-- {
		if created[i] != nil {
			if err := rs.deleteResourceRecord(created[i].ID, created[i].EventSectionID); err != nil {
				slog.Error("upload rollback failed to delete resource record", "resource_id", created[i].ID, "error", err)
			}
		}
	}
	for i := len(uploadedNames) - 1; i >= 0; i-- {
		if err := storage.DeleteFile(uploadedNames[i], folder, rs.Bucket, rs.Provider); err != nil {
			slog.Error("upload rollback failed to delete object", "folder", folder, "filename", uploadedNames[i], "error", err)
		}
	}
}

// RollbackUploadedResources compensates a downstream batch mutation (for
// example Font creation) after Resource records and objects were committed.
func (rs *ResourceService) RollbackUploadedResources(resources []*models.Resource) {
	if rs == nil || len(resources) == 0 {
		return
	}
	storage, storageErr := rs.requireStorage()
	if storageErr != nil {
		slog.Error("upload rollback could not access object storage", "error", storageErr)
	}
	for i := len(resources) - 1; i >= 0; i-- {
		resource := resources[i]
		if resource == nil {
			continue
		}
		if err := rs.deleteResourceRecord(resource.ID, resource.EventSectionID); err != nil {
			slog.Error("upload rollback failed to delete resource record", "resource_id", resource.ID, "error", err)
		}
		if storageErr == nil {
			folder, filename := resourceStoragePathParts(resource.Path, rs.UploadPath)
			if filename != "" {
				if err := storage.DeleteFile(filename, folder, rs.Bucket, rs.Provider); err != nil {
					slog.Error("upload rollback failed to delete object", "resource_id", resource.ID, "path", resource.Path, "error", err)
				}
			}
		}
	}
}

func isFontUploadContentType(contentType string) bool {
	switch canonicalUploadContentType(contentType) {
	case "font/ttf", "font/otf", "font/woff", "font/woff2", "font/sfnt", "application/vnd.ms-fontobject":
		return true
	default:
		return false
	}
}

func (rs *ResourceService) DownloadFile(filename string) (io.ReadCloser, error) {
	storage, err := rs.requireStorage()
	if err != nil {
		return nil, err
	}
	return storage.GetFileStream(filename, rs.UploadPath, rs.Bucket, rs.Provider)
}

func isAllowed(contentType string) bool {
	return AllowedMimeTypes[canonicalUploadContentType(contentType)]
}

func requireImageUploadContentType(contentType string) error {
	normalized := canonicalUploadContentType(contentType)
	if strings.HasPrefix(normalized, "image/") {
		return nil
	}
	return services.ValidationError{Msg: fmt.Sprintf("unsupported image type: %s", contentType)}
}

func (rs *ResourceService) sanitizeAndOptimizeUpload(
	file multipart.File,
	header *multipart.FileHeader,
	forcedName string,
) (optimized []byte, finalName string, finalType string, err error) {

	contentType, err := detectUploadContentType(file, header)
	if err != nil {
		return nil, "", "", err
	}

	if !AllowedMimeTypes[contentType] {
		return nil, "", "", services.ValidationError{Msg: fmt.Sprintf("unsupported file type: %s", contentType)}
	}
	if header.Size > MaxFileSizeBytes {
		return nil, "", "", services.ValidationError{Msg: fmt.Sprintf("file size exceeds %d MB", MaxFileSizeMB)}
	}

	optimized, newContentType, err := rs.Optimizer.OptimizeIfImage(file, header, contentType)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to optimize image: %w", err)
	}

	// Usa filename forzado o genera UUID
	baseName := forcedName
	if baseName == "" {
		u, _ := uuid.NewV4()
		baseName = u.String()
	}

	finalName = updateFilenameExtension(baseName, newContentType)
	return optimized, finalName, newContentType, nil
}

func detectUploadContentType(file multipart.File, header *multipart.FileHeader) (string, error) {
	if file == nil || header == nil {
		return "", services.ValidationError{Msg: "uploaded file is required"}
	}
	declared := canonicalUploadContentType(header.Header.Get("Content-Type"))
	ext := ""
	if index := strings.LastIndex(header.Filename, "."); index >= 0 {
		ext = strings.ToLower(header.Filename[index+1:])
	}
	if declared == "" || declared == "application/octet-stream" {
		declared = canonicalUploadContentType(guessMimeType(ext))
	}

	probe := make([]byte, 512)
	read, readErr := file.Read(probe)
	if readErr != nil && readErr != io.EOF {
		return "", fmt.Errorf("failed to inspect uploaded file: %w", readErr)
	}
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return "", fmt.Errorf("failed to rewind uploaded file: %w", seekErr)
	}
	detected := canonicalUploadContentType(http.DetectContentType(probe[:read]))

	// DetectContentType cannot identify every modern camera/font format. Keep a
	// declared, allowlisted type only when the probe is generic. For recognized
	// payloads, the bytes win over a spoofable multipart header.
	if detected != "" && detected != "application/octet-stream" {
		if detected == "text/plain" && declared == "image/svg+xml" && ext == "svg" {
			return declared, nil
		}
		if AllowedMimeTypes[detected] || detected == "text/html; charset=utf-8" || detected == "text/html" {
			return detected, nil
		}
	}
	return declared, nil
}

func (rs *ResourceService) CreateResource(resource *models.Resource) error {
	repo, err := rs.requireRepo()
	if err != nil {
		return err
	}

	if err := repo.CreateResource(resource); err != nil {
		return err
	}

	return rs.InvalidateSectionResourceCache(resource.EventSectionID)
}

func (rs *ResourceService) deleteResourceRecord(resourceID uuid.UUID, sectionID *uuid.UUID) error {
	repo, err := rs.requireRepo()
	if err != nil {
		return err
	}

	if sectionID != nil {
		if versionedRepo, ok := repo.(resourceSectionVersionedDeleter); ok {
			if err := versionedRepo.DeleteResourceAndTouchSection(resourceID, *sectionID, time.Now().UTC()); err != nil {
				return err
			}
			return rs.InvalidateSectionResourceCache(sectionID)
		}
	}

	if err := repo.DeleteResource(resourceID); err != nil {
		return err
	}

	return rs.InvalidateSectionResourceCache(sectionID)
}

func UpdateResource(resource *models.Resource) error {
	if _resourceSvc == nil {
		return resourceServiceUnavailable()
	}
	return _resourceSvc.UpdateResource(resource)
}

func CreateResource(resource *models.Resource) error {
	if _resourceSvc == nil {
		return resourceServiceUnavailable()
	}
	return _resourceSvc.CreateResource(resource)
}

func DeleteResource(resourceID uuid.UUID, sectionID *uuid.UUID) error {
	if _resourceSvc == nil {
		return resourceServiceUnavailable()
	}
	return _resourceSvc.deleteResourceRecord(resourceID, sectionID)
}

func guessMimeType(ext string) string {
	switch ext {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "svg":
		return "image/svg+xml"
	case "webp":
		return "image/webp"
	case "heic":
		return "image/heic"
	case "heif":
		return "image/heif"
	case "avif":
		return "image/avif"

	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
	case "m4v":
		return "video/x-m4v"
	case "3gp":
		return "video/3gpp"
	case "avi":
		return "video/x-msvideo"
	case "mkv":
		return "video/x-matroska"

	case "mp3":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "wav":
		return "audio/wav"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"

	case "ttf":
		return "font/ttf"
	case "otf":
		return "font/otf"
	case "woff":
		return "font/woff"
	case "woff2":
		return "font/woff2"
	case "eot":
		return "application/vnd.ms-fontobject"
	case "sfnt":
		return "font/sfnt"

	default:
		return "application/octet-stream"
	}
}

func updateFilenameExtension(filename, newContentType string) string {
	ext := ""

	switch newContentType {
	case "image/webp":
		ext = ".webp"
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/svg+xml":
		ext = ".svg"
	case "image/heic":
		ext = ".heic"
	case "image/heif":
		ext = ".heif"
	case "image/avif":
		ext = ".avif"

	case "video/mp4":
		ext = ".mp4"
	case "video/webm":
		ext = ".webm"
	case "video/quicktime":
		ext = ".mov"
	case "video/x-m4v":
		ext = ".m4v"
	case "video/3gpp":
		ext = ".3gp"
	case "video/x-msvideo":
		ext = ".avi"
	case "video/x-matroska":
		ext = ".mkv"

	case "audio/mpeg":
		ext = ".mp3"
	case "audio/ogg":
		ext = ".ogg"
	case "audio/wav":
		ext = ".wav"
	case "audio/aac":
		ext = ".aac"
	case "audio/flac":
		ext = ".flac"

	case "font/woff2":
		ext = ".woff2"
	case "font/woff":
		ext = ".woff"
	case "font/ttf":
		ext = ".ttf"
	case "font/otf":
		ext = ".otf"
	case "application/vnd.ms-fontobject":
		ext = ".eot"
	case "font/sfnt":
		ext = ".sfnt"

	default:
		ext = ""
	}

	// Corta la extensión anterior si la hay
	dot := strings.LastIndex(filename, ".")
	if dot != -1 {
		filename = filename[:dot]
	}

	return filename + ext
}
