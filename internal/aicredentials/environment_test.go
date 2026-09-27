package aicredentials

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestLocalResolverUsesOnlyLocalFileAndNeverSecretsManager(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewLocalFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "ignored", []byte(`{"schema_version":1,"credentials":{"minimax":{"api_key":"test-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	called := false
	resolver, err := NewResolverForEnvironment(context.Background(), "local", "us-east-1", "", path, func(context.Context, string) (Store, error) {
		called = true
		return nil, errors.New("must not be called")
	})
	if err != nil || called {
		t.Fatalf("resolver=%v err=%v secrets factory called=%v", resolver != nil, err, called)
	}
	key, err := resolver.APIKey(context.Background(), "minimax")
	if err != nil || key != "test-key" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestEnvironmentSelectionRejectsCrossBoundaryCredentialStores(t *testing.T) {
	if _, err := NewResolverForEnvironment(context.Background(), "local", "", "bundle", "local.json", nil); err == nil {
		t.Fatal("local Secrets Manager configuration accepted")
	}
	if _, err := NewResolverForEnvironment(context.Background(), "production", "", "", "local.json", nil); err == nil {
		t.Fatal("production local file configuration accepted")
	}
}

func TestLocalFileStorePersistsBundleUpdates(t *testing.T) {
	store, err := NewLocalFileStore(filepath.Join(t.TempDir(), "nested", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "ignored", []byte(`{"schema_version":1,"credentials":{}}`)); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(store, "local-file", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.ReplaceAPIKey(context.Background(), "minimax", "replacement-test-key"); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(stored)
	if err != nil || bundle.Credentials["minimax"].APIKey != "replacement-test-key" {
		t.Fatalf("bundle=%#v err=%v", bundle, err)
	}
}
