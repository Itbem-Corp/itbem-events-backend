//go:build windows || !cgo

package services

import (
	"io"
	"mime/multipart"
)

type ImageOptimizerService struct{}

func NewImageOptimizerService() *ImageOptimizerService {
	return &ImageOptimizerService{}
}

var imageMimeTypes = map[string]bool{
	"image/jpeg":    true,
	"image/png":     true,
	"image/gif":     false,
	"image/webp":    false,
	"image/heic":    true,
	"image/heif":    true,
	"image/svg+xml": false,
}

func (s *ImageOptimizerService) OptimizeIfImage(file multipart.File, header *multipart.FileHeader, contentType string) ([]byte, string, error) {
	buf, err := io.ReadAll(file)
	return buf, contentType, err
}
