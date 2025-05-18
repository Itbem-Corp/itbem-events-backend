package services

import (
	"fmt"
	"github.com/h2non/bimg"
	"io"
	"mime/multipart"
	"strings"
)

type ImageOptimizerService struct{}

func NewImageOptimizerService() *ImageOptimizerService {
	return &ImageOptimizerService{}
}

var imageMimeTypes = map[string]bool{
	"image/jpeg":    true,
	"image/png":     true,
	"image/gif":     true,
	"image/webp":    true,
	"image/heic":    true,  // 👈 nuevo
	"image/heif":    true,  // 👈 nuevo
	"image/svg+xml": false, // SVG no lo toca (texto)
}

func (s *ImageOptimizerService) OptimizeIfImage(file multipart.File, header *multipart.FileHeader, contentType string) ([]byte, string, error) {
	// Solo procesamos imágenes comunes
	if !imageMimeTypes[contentType] {
		// No se procesa, se regresa el contenido original tal cual
		buf, err := io.ReadAll(file)
		return buf, contentType, err
	}

	// Leer el contenido del archivo
	buf, err := io.ReadAll(file)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image: %w", err)
	}

	// Procesar con bimg
	image := bimg.NewImage(buf)
	options := bimg.Options{
		Quality:       75,
		Compression:   9,
		StripMetadata: true,
		// Width:         1500,      // si quieres conservar resolución, quítalo
		Type: bimg.WEBP, // o bimg.JPEG si prefieres
	}

	newImage, err := image.Process(options)
	if err != nil {
		if strings.Contains(err.Error(), "Unsupported") {
			return nil, "", fmt.Errorf("image format not supported on this system: %s", contentType)
		}
		return nil, "", fmt.Errorf("image optimization failed: %w", err)
	}

	return newImage, "image/webp", nil
}
