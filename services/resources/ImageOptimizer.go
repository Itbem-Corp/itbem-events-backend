//go:build !windows && cgo

package services

import (
	"fmt"
	"github.com/h2non/bimg"
	"io"
	"mime/multipart"
	"strings"
)

type ImageOptimizerService struct{}

type ResponsiveImageVariant struct {
	Width int
	Bytes []byte
}

func NewImageOptimizerService() *ImageOptimizerService {
	return &ImageOptimizerService{}
}

var imageMimeTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	// Preserve animation and avoid a second lossy pass over WebP files that
	// browsers have already optimized before upload.
	"image/gif":     false,
	"image/webp":    false,
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
		// WebP 84 keeps fine detail in event photography while compression 6
		// avoids the steep CPU/latency cost of the maximum effort setting.
		Quality:       84,
		Compression:   6,
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

func (s *ImageOptimizerService) ResponsiveWebPVariants(content []byte, widths []int, quality int) ([]ResponsiveImageVariant, error) {
	image := bimg.NewImage(content)
	size, err := image.Size()
	if err != nil {
		return nil, fmt.Errorf("read responsive image dimensions: %w", err)
	}
	result := make([]ResponsiveImageVariant, 0, len(widths))
	for _, width := range widths {
		if width <= 0 || width > size.Width {
			continue
		}
		processed, processErr := image.Process(bimg.Options{
			Width: width, Type: bimg.WEBP, Quality: quality,
			Compression: 6, StripMetadata: true,
		})
		if processErr != nil {
			return nil, fmt.Errorf("generate %dpx responsive image: %w", width, processErr)
		}
		result = append(result, ResponsiveImageVariant{Width: width, Bytes: processed})
	}
	return result, nil
}

func (s *ImageOptimizerService) ImageWidth(content []byte) (int, error) {
	size, err := bimg.NewImage(content).Size()
	if err != nil {
		return 0, fmt.Errorf("read image dimensions: %w", err)
	}
	return size.Width, nil
}
