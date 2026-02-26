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

// CompletedPart holds the PartNumber and ETag for one uploaded multipart segment.
type CompletedPart struct {
	PartNumber int
	ETag       string
}

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

// GetPresignedUploadURL returns a short-lived presigned PUT URL the browser can use
// to upload a file directly to S3.
func GetPresignedUploadURL(filename, folder, contentType, bucket, provider string, minutes int) (string, error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GeneratePresignedPutURL(ctx, objectKey, bucket, contentType, minutes)
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

// GetFileSize returns the byte size of a file in the selected cloud provider.
// Returns an error if the object does not exist.
func GetFileSize(filename, folder, bucket, provider string) (int64, error) {
	size, _, err := GetFileMeta(filename, folder, bucket, provider)
	return size, err
}

// GetFileMeta returns the byte size and Content-Type of a file in a single
// round-trip. Prefer this over GetFileSize when you also need the content type.
func GetFileMeta(filename, folder, bucket, provider string) (size int64, contentType string, err error) {
	ctx := context.Background()
	objectKey := fmt.Sprintf("%s/%s", folder, filename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GetS3ObjectMeta(ctx, objectKey, bucket)
	default:
		return 0, "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// CopyFile performs a server-side copy of a file within the same bucket.
func CopyFile(srcFilename, srcFolder, dstFilename, dstFolder, bucket, provider string) error {
	ctx := context.Background()
	srcKey := fmt.Sprintf("%s/%s", srcFolder, srcFilename)
	dstKey := fmt.Sprintf("%s/%s", dstFolder, dstFilename)

	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.CopyS3Object(ctx, srcKey, dstKey, bucket)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
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

// CreateMultipartUpload initiates a multipart upload. key is the full S3 object key
// (e.g. "moments/{eventID}/raw/{uuid}.mp4"). Returns the upload ID.
func CreateMultipartUpload(key, bucket, contentType, provider string) (string, error) {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.CreateMultipartUpload(ctx, key, bucket, contentType)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// GetPresignedPartURL signs a URL for uploading one specific part.
// partNumber is 1-based (S3 requirement). ttlMin is the URL lifetime in minutes.
func GetPresignedPartURL(key, bucket, uploadID string, partNumber, ttlMin int, provider string) (string, error) {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.GetPresignedPartURL(ctx, key, bucket, uploadID, partNumber, ttlMin)
	default:
		return "", fmt.Errorf("unsupported provider: %s", provider)
	}
}

// CompleteMultipartUpload assembles all uploaded parts into the final S3 object.
func CompleteMultipartUpload(key, bucket, uploadID string, parts []CompletedPart, provider string) error {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		awsParts := make([]awsrepository.CompletedPart, len(parts))
		for i, p := range parts {
			awsParts[i] = awsrepository.CompletedPart{PartNumber: p.PartNumber, ETag: p.ETag}
		}
		return awsrepository.CompleteMultipartUpload(ctx, key, bucket, uploadID, awsParts)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}

// AbortMultipartUpload cancels a multipart upload, freeing all uploaded parts.
func AbortMultipartUpload(key, bucket, uploadID, provider string) error {
	ctx := context.Background()
	switch strings.ToLower(provider) {
	case "aws":
		return awsrepository.AbortMultipartUpload(ctx, key, bucket, uploadID)
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
}
