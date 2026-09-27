package aicredentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LocalFileStore is intentionally restricted to local development. It keeps a
// disposable test bundle outside source control and does not initialize an AWS
// client or make a network request. It is not a production secret store.
type LocalFileStore struct{ path string }

func NewLocalFileStore(path string) (*LocalFileStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("local AI credential file is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("local AI credential file is invalid")
	}
	return &LocalFileStore{path: abs}, nil
}

func (s *LocalFileStore) Get(_ context.Context, _ string) ([]byte, error) {
	if s == nil || s.path == "" {
		return nil, errors.New("local AI credential storage is not configured")
	}
	value, err := os.ReadFile(s.path)
	if err != nil || len(value) == 0 || len(value) > maxBundleBytes {
		return nil, errors.New("local AI credential bundle is unavailable")
	}
	return value, nil
}

func (s *LocalFileStore) Put(_ context.Context, _ string, value []byte) error {
	if s == nil || s.path == "" || len(value) == 0 || len(value) > maxBundleBytes {
		return errors.New("local AI credential storage input is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return errors.New("local AI credential bundle could not be stored")
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".ai-credentials-*")
	if err != nil {
		return errors.New("local AI credential bundle could not be stored")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return errors.New("local AI credential bundle could not be stored")
	}
	if _, err := temporary.Write(value); err != nil {
		temporary.Close()
		return errors.New("local AI credential bundle could not be stored")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("local AI credential bundle could not be stored")
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		return errors.New("local AI credential bundle could not be stored")
	}
	_ = os.Chmod(s.path, 0600) // Best effort on Windows; no secret is logged.
	return nil
}
