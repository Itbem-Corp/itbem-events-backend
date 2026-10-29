package aicredentials

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ProtectedFileStore is a persistent self-hosted credential store. Mount the
// directory, not the file: replacing the file atomically must survive restarts.
// Provider keys remain confined to the API process and its private filesystem.
type ProtectedFileStore struct{ path string }

func NewProtectedFileStore(path string) (*ProtectedFileStore, error) {
	path = strings.TrimSpace(path)
	if runtime.GOOS == "windows" || !filepath.IsAbs(path) {
		return nil, errors.New("protected AI credential storage requires an absolute path on a Unix server")
	}
	s := &ProtectedFileStore{path: filepath.Clean(path)}
	raw, err := s.Get(context.Background(), "")
	if err != nil {
		return nil, err
	}
	if _, err := Parse(raw); err != nil {
		return nil, errors.New("protected AI credential bundle is invalid")
	}
	return s, nil
}

func (s *ProtectedFileStore) check() (os.FileInfo, error) {
	if s == nil || s.path == "" {
		return nil, errors.New("protected AI credential storage is not configured")
	}
	dir := filepath.Dir(s.path)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, errors.New("protected AI credential directory must not use symbolic links")
	}
	d, err := os.Stat(dir)
	if err != nil || !d.IsDir() || d.Mode().Perm() != 0700 {
		return nil, errors.New("protected AI credential directory must have mode 0700")
	}
	f, err := os.Lstat(s.path)
	if err != nil || !f.Mode().IsRegular() || f.Mode().Perm() != 0600 || f.Size() <= 0 || f.Size() > maxBundleBytes {
		return nil, errors.New("protected AI credential bundle must be an existing regular file with mode 0600 and a valid size")
	}
	return f, nil
}

func (s *ProtectedFileStore) Get(_ context.Context, _ string) ([]byte, error) {
	expected, err := s.check()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, errors.New("protected AI credential bundle is unavailable")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(expected, actual) || !actual.Mode().IsRegular() || actual.Mode().Perm() != 0600 {
		return nil, errors.New("protected AI credential bundle changed during access")
	}
	value, err := io.ReadAll(io.LimitReader(f, maxBundleBytes+1))
	if err != nil || len(value) == 0 || len(value) > maxBundleBytes {
		return nil, errors.New("protected AI credential bundle is unavailable")
	}
	return value, nil
}

func (s *ProtectedFileStore) Put(_ context.Context, _ string, value []byte) error {
	if _, err := s.check(); err != nil {
		return err
	}
	if _, err := Parse(value); err != nil {
		return errors.New("protected AI credential bundle input is invalid")
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".ai-credentials-*")
	if err != nil {
		return errors.New("protected AI credential bundle could not be stored")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return errors.New("protected AI credential bundle permissions could not be set")
	}
	if _, err := f.Write(value); err != nil {
		return errors.New("protected AI credential bundle could not be stored")
	}
	if err := f.Sync(); err != nil {
		return errors.New("protected AI credential bundle could not be synchronized")
	}
	if err := f.Close(); err != nil {
		return errors.New("protected AI credential bundle could not be stored")
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return errors.New("protected AI credential bundle could not be replaced")
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return errors.New("protected AI credential directory could not be synchronized")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return errors.New("protected AI credential directory could not be synchronized")
	}
	return nil
}
