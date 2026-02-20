package services

import (
	"events-stocks/repositories/bucketrepository"
	"fmt"
	"github.com/gofrs/uuid"
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

	// Subida física al bucket
	err = bucketrepository.UploadRawBytesSimple(content, filename, contentType, folder, rs.Bucket, rs.Provider)
	if err != nil {
		return "", "", err
	}

	// Generamos URL firmada para respuesta inmediata
	url, _ := bucketrepository.GetPresignedFileURL(filename, folder, rs.Bucket, rs.Provider, 720)

	// Retornamos el NOMBRE (ej: "ad9e2e0c...webp") y la URL
	return filename, url, nil
}

// 2. Modificar GetPresignedURL para Clientes (Helper adicional)
func (rs *ResourceService) GetClientLogoURL(clientID uuid.UUID, logoName string) string {
	if logoName == "" {
		return ""
	}
	folder := fmt.Sprintf("clients/%s/logo", clientID)
	url, _ := bucketrepository.GetPresignedFileURL(logoName, folder, rs.Bucket, rs.Provider, 720)
	return url
}
