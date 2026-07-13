package services

import (
	"fmt"
	"github.com/gofrs/uuid"
	"mime/multipart"
	"strings"
)

// UploadAvatar sube el avatar a una subcarpeta específica del usuario.
// Estructura final en S3: users/{uuid}/avatar/foto.webp
func (rs *ResourceService) UploadAvatar(
	file multipart.File,
	header *multipart.FileHeader,
	userID uuid.UUID,
) (string, error) {
	// 1. Definir la carpeta exacta incluyendo "avatar"
	// Esto crea la ruta: users/550e8.../avatar
	userFolder := fmt.Sprintf("users/%s/avatar", userID.String())

	// 2. Usamos el nombre original del archivo
	// (Ya no necesitamos forzar prefijos ni rutas aquí)
	// A fresh UUID key prevents name collisions and stale cached avatars.
	forcedFilename := ""

	// 3. Sanitizar y Optimizar
	// El optimizador convertirá a .webp si es imagen, manteniendo el nombre base
	optimized, finalName, finalType, err := rs.sanitizeAndOptimizeUpload(file, header, forcedFilename)
	if err != nil {
		return "", fmt.Errorf("avatar processing failed: %w", err)
	}
	if err := requireImageUploadContentType(finalType); err != nil {
		return "", fmt.Errorf("avatar processing failed: %w", err)
	}

	// 4. Subir a S3
	// El repositorio concatenará: userFolder + "/" + finalName
	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	err = storage.UploadRawBytesSimple(
		optimized,
		finalName,
		finalType,
		userFolder,
		rs.Bucket,
		rs.Provider,
	)

	if err != nil {
		return "", fmt.Errorf("failed to upload avatar: %w", err)
	}

	// 5. Retornar el PATH RELATIVO para la DB
	// Retorna: "users/550e8.../avatar/foto.webp"
	return fmt.Sprintf("%s/%s", userFolder, finalName), nil
}

// GetAvatarPresignedURL recibe el path de la DB y genera el link temporal
func (rs *ResourceService) GetAvatarPresignedURL(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	// Separamos carpeta y archivo porque tu bucketrepository pide filename y folder separados
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid avatar path format")
	}

	filename := parts[len(parts)-1]
	folder := strings.Join(parts[:len(parts)-1], "/")

	// Generamos URL firmada por 720 minutos (12 horas)
	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	return storage.GetPresignedFileURL(filename, folder, rs.Bucket, rs.Provider, 720)
}
