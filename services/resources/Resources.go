package services

import (
	"events-stocks/configuration/constants"
	"events-stocks/dtos"
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/repositories/cacheloaderrepository"
	"events-stocks/repositories/redisrepository"
	"events-stocks/repositories/resourcerepository"
	services "events-stocks/services/validations"
	"events-stocks/utils"
	"fmt"
	"github.com/gofrs/uuid"
	"io"
	"mime/multipart"
	"strings"
)

const (
	MaxFileSizeMB     = 8
	MaxFileSizeBytes  = MaxFileSizeMB * 1024 * 1024
	ErrPrefixValidate = "validation_error:"
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

	// Fonts
	"font/ttf":                      true,
	"font/otf":                      true,
	"font/woff":                     true,
	"font/woff2":                    true,
	"application/vnd.ms-fontobject": true,
	"font/sfnt":                     true,
}

type ResourceService struct {
	Bucket     string
	Provider   string
	UploadPath string // e.g. "resources"
	Optimizer  *ImageOptimizerService
}

func NewResourceService(c *models.Config) *ResourceService {
	return &ResourceService{
		Bucket:     c.AwsBucketName,
		Provider:   constants.DefaultCloudProvider,
		UploadPath: constants.EventsBucketFolder,
		Optimizer:  NewImageOptimizerService(),
	}
}

func (rs *ResourceService) GetResourceByID(id uuid.UUID) (*models.Resource, string, error) {
	resource, err := resourcerepository.GetResourceByID(id)
	if err != nil {
		return nil, "", fmt.Errorf("resource not found: %w", err)
	}

	// Asegúrate de que resource.Path exista
	if strings.TrimSpace(resource.Path) == "" {
		return nil, "", fmt.Errorf("resource has no path assigned")
	}

	parts := strings.Split(resource.Path, "/")
	filename := parts[len(parts)-1]

	exists, _, err := bucketrepository.FileExists(filename, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return nil, "", fmt.Errorf("failed to verify file existence: %w", err)
	}
	if !exists {
		return nil, "", fmt.Errorf("file associated with resource not found in bucket")
	}

	viewURL, err := bucketrepository.GetPresignedFileURL(filename, rs.UploadPath, rs.Bucket, rs.Provider, 720)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return resource, viewURL, nil
}

func (rs *ResourceService) GetResourcesBySectionID(sectionID uuid.UUID) ([]dtos.ResourceResponse, error) {
	return cacheloaderrepository.GetResourcesBySectionID(&sectionID, rs.UploadPath, rs.Bucket, rs.Provider)
}

func (rs *ResourceService) FileExists(filename string) (bool, string, error) {
	return bucketrepository.FileExists(filename, rs.UploadPath, rs.Bucket, rs.Provider)
}

func (rs *ResourceService) DeleteFileIfExists(filename string) error {
	exists, _, err := bucketrepository.FileExists(filename, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return fmt.Errorf("error checking file: %w", err)
	}
	if !exists {
		return fmt.Errorf("file does not exist")
	}

	if err := bucketrepository.DeleteFile(filename, rs.UploadPath, rs.Bucket, rs.Provider); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	return nil
}

func (rs *ResourceService) DeleteResource(id uuid.UUID) error {
	// 🔍 Obtener el recurso desde DB
	resource, err := resourcerepository.GetResourceByID(id)
	if err != nil {
		return fmt.Errorf("resource not found: %w", err)
	}

	// 📦 Eliminar archivo del bucket
	parts := strings.Split(resource.Path, "/")
	filename := parts[len(parts)-1]

	if err := rs.DeleteFileIfExists(filename); err != nil {
		return fmt.Errorf("failed to delete file from bucket: %w", err)
	}

	// 🗑️ Eliminar registro en DB
	if err := DeleteResource(id, resource.EventSectionID); err != nil {
		return fmt.Errorf("failed to delete resource from DB: %w", err)
	}

	// 🔁 Reordenar posiciones de la misma sección
	resources, err := resourcerepository.ListResourcesBySection(resource.EventSectionID)
	if err == nil {
		for i, r := range resources {
			r.Position = utils.PtrInt(i)
			_ = UpdateResource(&r)
		}
	}

	return nil
}

func (rs *ResourceService) UpdateResource(resource *models.Resource) error {
	return UpdateResource(resource)
}

func (rs *ResourceService) UpdateFileContent(
	file multipart.File,
	filename string,
	header *multipart.FileHeader,
) (string, error) {
	optimized, newFilename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, filename)
	if err != nil {
		return "", err
	}

	_, err = bucketrepository.UpdateFile(optimized, newFilename, contentType, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to update file: %w", err)
	}

	return fmt.Sprintf("%s/%s", rs.UploadPath, newFilename), nil
}

func (rs *ResourceService) ListResourcesBySection(sectionID *uuid.UUID) ([]models.Resource, error) {
	return resourcerepository.ListResourcesBySection(sectionID)
}

func (rs *ResourceService) ReplaceFile(
	oldFilename string,
	file multipart.File,
	header *multipart.FileHeader,
) (string, error) {
	if err := rs.DeleteFileIfExists(oldFilename); err != nil {
		return "", fmt.Errorf("failed to delete existing file: %w", err)
	}

	optimized, newFilename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, oldFilename)
	if err != nil {
		return "", err
	}

	err = bucketrepository.UploadRawBytesSimple(optimized, newFilename, contentType, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to upload replacement: %w", err)
	}

	return fmt.Sprintf("%s/%s", rs.UploadPath, newFilename), nil
}

func (rs *ResourceService) UploadAndCreateResource(
	file multipart.File,
	header *multipart.FileHeader,
	sectionID *uuid.UUID,
	resourceTypeID uuid.UUID,
	altText, title string,
) (*models.Resource, error) {
	existing, err := resourcerepository.ListResourcesBySection(sectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing resources: %w", err)
	}

	position := 0
	for _, r := range existing {
		if r.Position != nil && *r.Position >= position {
			position = *r.Position + 1
		}
	}

	optimized, filename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return nil, err
	}

	err = bucketrepository.UploadRawBytesSimple(optimized, filename, contentType, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to upload resource: %w", err)
	}

	resource := &models.Resource{
		EventSectionID: sectionID,
		ResourceTypeID: resourceTypeID,
		Path:           fmt.Sprintf("%s/%s", rs.UploadPath, filename),
		AltText:        altText,
		Title:          title,
		Position:       utils.PtrInt(position),
	}

	if err := CreateResource(resource); err != nil {
		return nil, fmt.Errorf("failed to create resource in DB: %w", err)
	}

	return resource, nil
}

func (rs *ResourceService) UploadMultipleResources(
	files []*multipart.FileHeader,
	sectionID *uuid.UUID,
	resourceTypeID uuid.UUID,
) ([]*models.Resource, error) {
	var uploaded []*models.Resource

	for i, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, fmt.Errorf("error opening file %d: %w", i+1, err)
		}
		defer file.Close()

		forcedFilename := fmt.Sprintf("resource-%s-%d%s", sectionID.String(), i+1, header.Filename)

		content, finalName, finalType, err := rs.sanitizeAndOptimizeUpload(file, header, forcedFilename)
		if err != nil {
			return nil, fmt.Errorf("failed to process file %d: %w", i+1, err)
		}

		err = bucketrepository.UploadRawBytesSimple(content, finalName, finalType, rs.UploadPath, rs.Bucket, rs.Provider)
		if err != nil {
			return nil, fmt.Errorf("upload failed for file %d: %w", i+1, err)
		}

		resource := &models.Resource{
			EventSectionID: sectionID,
			ResourceTypeID: resourceTypeID,
			Path:           fmt.Sprintf("%s/%s", rs.UploadPath, finalName),
			AltText:        "",
			Title:          finalName,
			Position:       utils.PtrInt(i),
		}

		if err := CreateResource(resource); err != nil {
			return nil, fmt.Errorf("failed to save resource for file %d: %w", i+1, err)
		}

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
	resourceTypes, err := ListResourceTypes()
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
	for i, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, fmt.Errorf("error opening file %d: %w", i+1, err)
		}
		defer file.Close()

		// Nombre forzado si se requiere
		forcedFilename := fmt.Sprintf("base-%s-%d%s", resourceTypeName, i+1, header.Filename)

		// 3. Sanitizar y optimizar
		content, finalName, finalType, err := rs.sanitizeAndOptimizeUpload(file, header, forcedFilename)
		if err != nil {
			return nil, fmt.Errorf("failed to process file %d: %w", i+1, err)
		}

		// 4. Upload en subfolder
		finalPath := fmt.Sprintf("%s/%s", subfolder, finalName)
		err = bucketrepository.UploadRawBytesSimple(content, finalName, finalType, subfolder, rs.Bucket, rs.Provider)
		if err != nil {
			return nil, fmt.Errorf("upload failed for file %d: %w", i+1, err)
		}

		// 5. Crear modelo sin section
		resource := &models.Resource{
			ResourceTypeID: resourceTypeID,
			Path:           finalPath,
			AltText:        "",
			Title:          finalName,
		}

		if err := CreateResource(resource); err != nil {
			return nil, fmt.Errorf("failed to save resource for file %d: %w", i+1, err)
		}

		uploaded = append(uploaded, resource)
	}

	return uploaded, nil
}

func (rs *ResourceService) DownloadFile(filename string) (io.ReadCloser, error) {
	return bucketrepository.GetFileStream(filename, rs.UploadPath, rs.Bucket, rs.Provider)
}

func isAllowed(contentType string) bool {
	return AllowedMimeTypes[contentType]
}

func (rs *ResourceService) sanitizeAndOptimizeUpload(
	file multipart.File,
	header *multipart.FileHeader,
	forcedName string,
) (optimized []byte, finalName string, finalType string, err error) {

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		ext := strings.ToLower(header.Filename[strings.LastIndex(header.Filename, ".")+1:])
		contentType = guessMimeType(ext)
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

func UpdateResource(resource *models.Resource) error {
	err := resourcerepository.UpdateResource(resource)
	if err != nil {
		return err
	}

	// Invalidar cache de esa sección
	return redisrepository.Invalidate("resources", resource.EventSectionID.String())
}

func CreateResource(resource *models.Resource) error {
	err := resourcerepository.CreateResource(resource)
	if err != nil {
		return err
	}

	return redisrepository.Invalidate("resources", resource.EventSectionID.String())
}

func DeleteResource(resourceID uuid.UUID, sectionID *uuid.UUID) error {
	err := resourcerepository.DeleteResource(resourceID)
	if err != nil {
		return err
	}

	return redisrepository.Invalidate("resources", sectionID.String())
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
	case "heic", "heif":
		return "image/heic"

	case "mp4":
		return "video/mp4"
	case "webm":
		return "video/webm"
	case "mov":
		return "video/quicktime"
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

	case "video/mp4":
		ext = ".mp4"
	case "video/webm":
		ext = ".webm"
	case "video/quicktime":
		ext = ".mov"
	case "video/x-msvideo":
		ext = ".avi"
	case "video/x-matroska":
		ext = ".mkv"

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
