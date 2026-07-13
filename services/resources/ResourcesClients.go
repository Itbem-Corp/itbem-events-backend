package services

import (
	"fmt"
	"github.com/gofrs/uuid"
	"log/slog"
	"mime/multipart"
)

func (rs *ResourceService) UploadClientLogo(file multipart.File, header *multipart.FileHeader, clientID uuid.UUID) (string, string, error) {
	// Definimos la carpeta dinámica: clients/UUID/logo/
	folder := fmt.Sprintf("clients/%s/logo", clientID)

	// Al pasar "" forzamos la generación de un UUID nuevo
	content, filename, contentType, err := rs.sanitizeAndOptimizeUpload(file, header, "")
	if err != nil {
		return "", "", err
	}
	if err := requireImageUploadContentType(contentType); err != nil {
		return "", "", err
	}

	// Subida física al bucket
	storage, err := rs.requireStorage()
	if err != nil {
		return "", "", err
	}
	err = storage.UploadRawBytesSimple(content, filename, contentType, folder, rs.Bucket, rs.Provider)
	if err != nil {
		if cleanupErr := storage.DeleteFile(filename, folder, rs.Bucket, rs.Provider); cleanupErr != nil {
			slog.Error("client logo upload rollback failed", "client_id", clientID, "filename", filename, "error", cleanupErr)
		}
		return "", "", err
	}

	// Generamos URL firmada para respuesta inmediata
	url, _ := storage.GetPresignedFileURL(filename, folder, rs.Bucket, rs.Provider, 720)

	// Retornamos el NOMBRE (ej: "ad9e2e0c...webp") y la URL
	return filename, url, nil
}

// 2. Modificar GetPresignedURL para Clientes (Helper adicional)
func (rs *ResourceService) GetClientLogoURL(clientID uuid.UUID, logoName string) string {
	if logoName == "" {
		return ""
	}
	folder := fmt.Sprintf("clients/%s/logo", clientID)
	storage, err := rs.requireStorage()
	if err != nil {
		return ""
	}
	url, _ := storage.GetPresignedFileURL(logoName, folder, rs.Bucket, rs.Provider, 720)
	return url
}
