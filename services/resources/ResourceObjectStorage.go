package services

import "fmt"

// GetPresignedURL returns a view URL using the standard resource TTL.
func GetPresignedURL(path, bucket, provider string) (string, error) {
	return GetPresignedURLWithTTL(path, bucket, provider, ResourceViewURLTTLMinutes)
}

func GetPresignedURLWithTTL(path, bucket, provider string, ttlMinutes int64) (string, error) {
	if _resourceSvc == nil {
		return "", resourceServiceUnavailable()
	}
	return _resourceSvc.getPresignedURLWithTTL(path, bucket, provider, ttlMinutes)
}

func (rs *ResourceService) getPresignedURLWithTTL(path, bucket, provider string, ttlMinutes int64) (string, error) {
	if path == "" {
		return "", nil
	}
	folder, filename := resourceStoragePathParts(path, "")
	if folder == "" || filename == "" {
		return "", fmt.Errorf("invalid path format: %s", path)
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return "", err
	}
	return storage.GetPresignedFileURL(filename, folder, bucket, provider, int(ttlMinutes))
}

func (rs *ResourceService) GetPresignedURL(path string) (string, error) {
	return rs.getPresignedURLWithTTL(path, rs.Bucket, rs.Provider, ResourceViewURLTTLMinutes)
}

func (rs *ResourceService) GetPresignedURLWithTTL(path string, ttlMinutes int64) (string, error) {
	return rs.getPresignedURLWithTTL(path, rs.Bucket, rs.Provider, ttlMinutes)
}

// DeleteObjectByPath deletes a storage object when it exists. The existence
// check preserves compatibility with storage adapters that surface a missing
// object as an error.
func (rs *ResourceService) DeleteObjectByPath(fullPath string) error {
	if fullPath == "" {
		return nil
	}
	folder, filename := resourceStoragePathParts(fullPath, "")
	if filename == "" {
		return fmt.Errorf("invalid path for deletion: %s", fullPath)
	}
	storage, err := rs.requireStorage()
	if err != nil {
		return err
	}
	exists, _, err := storage.FileExists(filename, folder, rs.Bucket, rs.Provider)
	if err != nil {
		return fmt.Errorf("error verifying file: %w", err)
	}
	if !exists {
		return nil
	}
	if err := storage.DeleteFile(filename, folder, rs.Bucket, rs.Provider); err != nil {
		return fmt.Errorf("failed to remove object from bucket: %w", err)
	}
	return nil
}

func (rs *ResourceService) DeleteObjectByPathFromBucket(fullPath, bucket string) error {
	return rs.WithBucket(bucket).DeleteObjectByPath(fullPath)
}
