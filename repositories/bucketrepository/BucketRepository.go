package bucketrepository

import (
	"context"
	"events-stocks/dtos"
	"events-stocks/repositories/awsrepository"
	"events-stocks/services/ports"
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

func GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GeneratePresignedPutURL(ctx, objectKey, bucket, contentType, minutes)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.CreateMultipartUpload(ctx, objectKey, bucket, contentType)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GeneratePresignedUploadPartURL(ctx, objectKey, bucket, uploadID, partNumber, minutes)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

func CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.CompleteMultipartUpload(ctx, objectKey, bucket, uploadID, parts)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

func AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.AbortMultipartUpload(ctx, objectKey, bucket, uploadID)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

// UploadFile uploads a multipart file to the selected cloud provider
func UploadFile(file multipart.File, fileHeader *multipart.FileHeader, folder string, bucket string, provider string) (string, error) {
	ctx := context.Background()

	fileExt := filepath.Ext(fileHeader.Filename)
	fileName := fmt.Sprintf("%s%s", uuid.New().String(), fileExt)

	if folder == "" {
		folder = defaultUploadDir
	}
	objectKey := fmt.Sprintf("%s/%s", folder, fileName)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.UploadStreamToS3(ctx, file, fileHeader.Size, objectKey, fileHeader.Header.Get("Content-Type"), bucket)
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

func GetObjectMetadata(filename, folder, bucket, provider string) (ports.ObjectStorageMetadata, error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)
	switch strings.ToLower(provider) {
	case "aws":
		size, contentType, err := awsrepository.GetS3ObjectMetadata(ctx, objectKey, bucket)
		return ports.ObjectStorageMetadata{Size: size, ContentType: contentType}, err
	default:
		return ports.ObjectStorageMetadata{}, fmt.Errorf("unsupported provider: %s", provider)
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

func UploadStream(ctx context.Context, body io.Reader, contentLength int64, filename, contentType, folder, bucket, provider string) error {
	if folder == "" {
		folder = defaultUploadDir
	}
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		_, err := awsrepository.UploadStreamToS3(ctx, body, contentLength, objectKey, contentType, bucket)
		return err
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

func MarkUploadConfirmed(ctx context.Context, filename, folder, bucket, provider string) error {
	objectKey := fmt.Sprintf("%s/%s", folder, filename)
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.MarkS3ObjectUploadConfirmed(ctx, objectKey, bucket)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

type BucketRepo struct{}

var _ ports.ObjectStorageStreamUploader = (*BucketRepo)(nil)
var _ ports.ObjectStorageUploadConfirmer = (*BucketRepo)(nil)

func NewBucketRepo() *BucketRepo { return &BucketRepo{} }

func (r *BucketRepo) FileExists(filename, folder, bucket, provider string) (bool, string, error) {
	return FileExists(filename, folder, bucket, provider)
}
func (r *BucketRepo) GetObjectMetadata(filename, folder, bucket, provider string) (ports.ObjectStorageMetadata, error) {
	return GetObjectMetadata(filename, folder, bucket, provider)
}
func (r *BucketRepo) GetPresignedFileURL(filename, folder, bucket, provider string, minutes int) (string, error) {
	return GetPresignedFileURL(filename, folder, bucket, provider, minutes)
}
func (r *BucketRepo) GetPresignedPutURL(objectKey, bucket, provider, contentType string, minutes int) (string, error) {
	return GetPresignedPutURL(objectKey, bucket, provider, contentType, minutes)
}
func (r *BucketRepo) CreateMultipartUpload(objectKey, bucket, provider, contentType string) (string, error) {
	return CreateMultipartUpload(objectKey, bucket, provider, contentType)
}
func (r *BucketRepo) GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID string, partNumber, minutes int) (string, error) {
	return GetPresignedUploadPartURL(objectKey, bucket, provider, uploadID, partNumber, minutes)
}
func (r *BucketRepo) CompleteMultipartUpload(objectKey, bucket, provider, uploadID string, parts []dtos.CompletedUploadPart) error {
	return CompleteMultipartUpload(objectKey, bucket, provider, uploadID, parts)
}
func (r *BucketRepo) AbortMultipartUpload(objectKey, bucket, provider, uploadID string) error {
	return AbortMultipartUpload(objectKey, bucket, provider, uploadID)
}
func (r *BucketRepo) UpdateFile(content []byte, filename, contentType, folder, bucket, provider string) (string, error) {
	return UpdateFile(content, filename, contentType, folder, bucket, provider)
}
func (r *BucketRepo) UploadRawBytesSimple(content []byte, filename, contentType, folder, bucket, provider string) error {
	return UploadRawBytesSimple(content, filename, contentType, folder, bucket, provider)
}
func (r *BucketRepo) UploadStream(ctx context.Context, body io.Reader, contentLength int64, filename, contentType, folder, bucket, provider string) error {
	return UploadStream(ctx, body, contentLength, filename, contentType, folder, bucket, provider)
}
func (r *BucketRepo) MarkUploadConfirmed(ctx context.Context, filename, folder, bucket, provider string) error {
	return MarkUploadConfirmed(ctx, filename, folder, bucket, provider)
}
func (r *BucketRepo) DeleteFile(filename, folder, bucket, provider string) error {
	return DeleteFile(filename, folder, bucket, provider)
}
func (r *BucketRepo) GetFileStream(filename, folder, bucket, provider string) (io.ReadCloser, error) {
	return GetFileStream(filename, folder, bucket, provider)
}
