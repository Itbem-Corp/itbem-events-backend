package services

import (
	"events-stocks/models"
	"events-stocks/repositories/bucketrepository"
	"events-stocks/repositories/resourcerepository"
	services "events-stocks/services/validations"
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

func NewResourceService(bucket, provider, path string) *ResourceService {
	return &ResourceService{
		Bucket:     bucket,
		Provider:   provider,
		UploadPath: path,
		Optimizer:  NewImageOptimizerService(),
	}
}

func (rs *ResourceService) GetResourceByID(id uuid.UUID) (*models.Resource, error) {
	resource, err := resourcerepository.GetResourceByID(id)
	if err != nil {
		return nil, fmt.Errorf("resource not found: %w", err)
	}

	// Extraer filename del URL (simplemente el path final)
	parts := strings.Split(resource.URL, "/")
	filename := parts[len(parts)-1]

	// Validar si el archivo existe en el bucket
	exists, _, err := bucketrepository.FileExists(filename, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to verify file existence: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("file associated with resource not found in bucket")
	}

	return resource, nil
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
	parts := strings.Split(resource.URL, "/")
	filename := parts[len(parts)-1]

	if err := rs.DeleteFileIfExists(filename); err != nil {
		return fmt.Errorf("failed to delete file from bucket: %w", err)
	}

	// 🗑️ Eliminar registro en DB
	if err := resourcerepository.DeleteResource(id); err != nil {
		return fmt.Errorf("failed to delete resource from DB: %w", err)
	}

	// 🔁 Reordenar posiciones de la misma sección
	resources, err := resourcerepository.ListResourcesBySection(resource.EventSectionID)
	if err == nil {
		for i, r := range resources {
			r.Position = i
			_ = resourcerepository.UpdateResource(&r)
		}
	}

	return nil
}

func (rs *ResourceService) UpdateResource(resource *models.Resource) error {
	return resourcerepository.UpdateResource(resource)
}

func (rs *ResourceService) UpdateFileContent(
	file multipart.File,
	filename string,
	header *multipart.FileHeader,
) (string, error) {
	// ✅ Valida, optimiza y renombra automáticamente
	optimized, newFilename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, filename)
	if err != nil {
		return "", err
	}

	// 🔁 Actualiza en bucket con el contenido optimizado
	url, err := bucketrepository.UpdateFile(optimized, newFilename, contentType, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to update file: %w", err)
	}

	return url, nil
}

func (rs *ResourceService) ListResourcesBySection(sectionID uuid.UUID) ([]models.Resource, error) {
	return resourcerepository.ListResourcesBySection(sectionID)
}

func (rs *ResourceService) ReplaceFile(
	oldFilename string,
	file multipart.File,
	header *multipart.FileHeader,
) (string, error) {
	// ✅ Elimina el archivo anterior si existe
	if err := rs.DeleteFileIfExists(oldFilename); err != nil {
		return "", fmt.Errorf("failed to delete existing file: %w", err)
	}

	// ✅ Valida, optimiza y renombra basado en el nombre viejo
	optimized, newFilename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, oldFilename)
	if err != nil {
		return "", err
	}

	// ✅ Sube el nuevo archivo optimizado
	url, err := bucketrepository.UploadRawBytes(optimized, newFilename, contentType, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return "", fmt.Errorf("failed to upload replacement: %w", err)
	}

	return url, nil
}

func (rs *ResourceService) UploadAndCreateResource(
	file multipart.File,
	header *multipart.FileHeader,
	sectionID uuid.UUID,
	resourceTypeID uuid.UUID,
	altText, title string,
) (*models.Resource, error) {
	// 🧮 Calcular posición automáticamente
	existing, err := resourcerepository.ListResourcesBySection(sectionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing resources: %w", err)
	}

	position := 0
	for _, r := range existing {
		if r.Position >= position {
			position = r.Position + 1
		}
	}

	// ⏬ Validación, optimización y renombrado en un solo paso
	optimized, filename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return nil, err // ya viene como ValidationError si aplica
	}

	// ⬆️ Subida al bucket
	url, err := bucketrepository.UploadRawBytes(optimized, filename, contentType, rs.UploadPath, rs.Bucket, rs.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to upload resource: %w", err)
	}

	// 🧱 Registro en base de datos
	resource := &models.Resource{
		EventSectionID: sectionID,
		ResourceTypeID: resourceTypeID,
		URL:            url,
		AltText:        altText,
		Title:          title,
		Position:       position,
	}

	if err := resourcerepository.CreateResource(resource); err != nil {
		return nil, fmt.Errorf("failed to create resource in DB: %w", err)
	}

	return resource, nil
}

func (rs *ResourceService) UploadMultipleResources(
	files []*multipart.FileHeader,
	sectionID uuid.UUID,
	resourceTypeID uuid.UUID,
) ([]*models.Resource, error) {

	var uploaded []*models.Resource

	for i, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, fmt.Errorf("error opening file %d: %w", i+1, err)
		}
		defer file.Close()

		// Nombre forzado por índice para mantener orden (puedes personalizarlo)
		forcedFilename := fmt.Sprintf("resource-%s-%d%s", sectionID.String(), i+1, header.Filename)

		// ⏬ Validación, optimización, renombrado
		content, finalName, finalType, err := rs.sanitizeAndOptimizeUpload(file, header, forcedFilename)
		if err != nil {
			return nil, fmt.Errorf("failed to process file %d: %w", i+1, err)
		}

		// ⬆️ Sube al bucket
		url, err := bucketrepository.UploadRawBytes(content, finalName, finalType, rs.UploadPath, rs.Bucket, rs.Provider)
		if err != nil {
			return nil, fmt.Errorf("upload failed for file %d: %w", i+1, err)
		}

		// 🧱 Crea en base de datos
		resource := &models.Resource{
			EventSectionID: sectionID,
			ResourceTypeID: resourceTypeID,
			URL:            url,
			AltText:        "", // puedes pasarlo como slice si quieres en un futuro
			Title:          "",
			Position:       i,
		}

		if err := resourcerepository.CreateResource(resource); err != nil {
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
