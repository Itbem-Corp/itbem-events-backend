//go:build windows || !cgo

package services

import (
	"io"
	"mime/multipart"
)

type ImageOptimizerService struct{}

type ResponsiveImageVariant struct {
	Width int
	Bytes []byte
}

func NewImageOptimizerService() *ImageOptimizerService {
	return &ImageOptimizerService{}
}

func (s *ImageOptimizerService) OptimizeIfImage(file multipart.File, header *multipart.FileHeader, contentType string) ([]byte, string, error) {
	buf, err := io.ReadAll(file)
	return buf, contentType, err
}

func (s *ImageOptimizerService) ResponsiveWebPVariants([]byte, []int, int) ([]ResponsiveImageVariant, error) {
	return nil, nil
}

func (s *ImageOptimizerService) ImageWidth([]byte) (int, error) {
	return 0, nil
}
