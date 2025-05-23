package bucketrepository

import (
	"bytes"
	"context"
	"events-stocks/repositories/awsrepository"
	"fmt"
	"github.com/google/uuid"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"
)

const defaultUploadDir = "uploads"

func GetPresignedFileURL(filename string, folder string, bucket string, provider string, minutes int) (string, error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GeneratePresignedURL(ctx, objectKey, bucket, minutes)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// UploadFile uploads a multipart file to the selected cloud provider
func UploadFile(file multipart.File, fileHeader *multipart.FileHeader, folder string, bucket string, provider string) (string, error) {
	ctx := context.Background()

	buffer := new(bytes.Buffer)
	_, err := buffer.ReadFrom(file)
	if err != nil {
		return "", err
	}

	fileExt := filepath.Ext(fileHeader.Filename)
	fileName := fmt.Sprintf("%s%s", uuid.New().String(), fileExt)

	if folder == "" {
		folder = defaultUploadDir
	}
	objectKey := fmt.Sprintf("%s/%s", folder, fileName)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.UploadToS3(ctx, buffer.Bytes(), objectKey, fileHeader.Header.Get("Content-Type"), bucket)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// UploadRawBytes uploads a byte array to the selected cloud provider
func UploadRawBytes(content []byte, filename string, contentType string, folder string, bucket string, provider string) (string, error) {
	ctx := context.Background()

	if folder == "" {
		folder = defaultUploadDir
	}
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.UploadToS3(ctx, content, objectKey, contentType, bucket)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// FileExists checks if a file exists in the selected cloud provider
func FileExists(filename string, folder string, bucket string, provider string) (bool, string, error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		exists, err := awsrepository.CheckS3ObjectExists(ctx, objectKey, bucket)
		if err != nil {
			return false, "", err
		}
		if exists {
			return true, awsrepository.GetS3URL(bucket, objectKey), nil
		}
		return false, "", nil
	default:
		return false, "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// UpdateFile replaces the content of a file in the selected cloud provider
func UpdateFile(content []byte, filename string, contentType string, folder string, bucket string, provider string) (string, error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.UploadToS3(ctx, content, objectKey, contentType, bucket)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// DeleteFile removes a file from the selected cloud provider
func DeleteFile(filename string, folder string, bucket string, provider string) error {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.DeleteS3Object(ctx, objectKey, bucket)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

// ListFilesInFolder lists files inside a folder in the selected cloud provider
func ListFilesInFolder(folder string, bucket string, provider string) ([]string, error) {
	ctx := context.Background()
	prefix := strings.TrimSuffix(folder, "/") + "/"

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.ListS3ObjectsWithPrefix(ctx, prefix, bucket)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// GetFileStream retrieves a file stream from the selected cloud provider
func GetFileStream(filename string, folder string, bucket string, provider string) (io.ReadCloser, error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GetS3Object(ctx, objectKey, bucket)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		_, err := awsrepository.UploadToS3(ctx, content, objectKey, contentType, bucket)
		return err
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}
