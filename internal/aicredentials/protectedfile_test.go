package aicredentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func protectedFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("production file permissions require Unix")
	}
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"credentials":{"minimax":{"api_key":"platform-test"}},"projects":{"11111111-1111-4111-8111-111111111111":{"credentials":{"minimax":{"api_key":"project-test"}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProductionFilePreservesProjectIsolationAndDurableUpdates(t *testing.T) {
	path := protectedFixture(t)
	factory := func(context.Context, string) (Store, error) {
		t.Fatal("AWS must not initialize")
		return nil, errors.New("unexpected AWS")
	}
	r, err := NewResolverForEnvironmentWithFile(context.Background(), "production", "us-east-2", "", "", path, factory)
	if err != nil {
		t.Fatal(err)
	}
	project := "11111111-1111-4111-8111-111111111111"
	if key, err := r.APIKeyForProject(context.Background(), project, "minimax"); err != nil || key != "project-test" {
		t.Fatal("project credential not preserved")
	}
	if _, err := r.APIKeyForProject(context.Background(), "22222222-2222-4222-8222-222222222222", "minimax"); err == nil {
		t.Fatal("project fell back to platform key")
	}
	if err := r.ReplaceProjectAPIKey(context.Background(), project, "minimax", "rotated-test"); err != nil {
		t.Fatal(err)
	}
	r, err = NewResolverForEnvironmentWithFile(context.Background(), "production", "", "", "", path, factory)
	if err != nil {
		t.Fatal(err)
	}
	if key, err := r.APIKeyForProject(context.Background(), project, "minimax"); err != nil || key != "rotated-test" {
		t.Fatal("rotation did not survive restart")
	}
	if key, err := r.APIKey(context.Background(), "minimax"); err != nil || key != "platform-test" {
		t.Fatal("project rotation changed platform key")
	}
	if err := r.ReplaceUsageAPIKey(context.Background(), "minimax", "usage-test"); err != nil {
		t.Fatal(err)
	}
	if key, err := r.UsageAPIKey(context.Background(), "minimax"); err != nil || key != "usage-test" {
		t.Fatal("usage credential not preserved")
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("rotation loosened file permissions")
	}
}

func TestProductionFileRejectsInsecureAndAmbiguousConfiguration(t *testing.T) {
	path := protectedFixture(t)
	for _, env := range []string{"local", "development", "test"} {
		if _, err := NewResolverForEnvironmentWithFile(context.Background(), env, "", "", "", path, nil); err == nil {
			t.Fatal("production file accepted in development")
		}
	}
	for _, pair := range [][2]string{{"secret", ""}, {"", "test.json"}} {
		if _, err := NewResolverForEnvironmentWithFile(context.Background(), "production", "", pair[0], pair[1], path, nil); err == nil {
			t.Fatal("multiple stores accepted")
		}
	}
	if _, err := NewProtectedFileStore("relative.json"); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProtectedFileStore(path); err == nil {
		t.Fatal("public file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProtectedFileStore(path); err == nil {
		t.Fatal("public directory accepted")
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewProtectedFileStore(link); err == nil {
		t.Fatal("symlink accepted")
	}
	s, err := NewProtectedFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte(`{"schema_version":99}`), []byte(strings.Repeat("x", maxBundleBytes+1))} {
		if err := s.Put(context.Background(), "", bad); err == nil {
			t.Fatal("invalid overwrite accepted")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed write changed credential bundle")
	}
}

