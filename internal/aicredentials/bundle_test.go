package aicredentials

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu     sync.Mutex
	value  []byte
	gets   int
	puts   int
	getErr error
}

func (s *memoryStore) Get(_ context.Context, _ string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.getErr != nil {
		return nil, s.getErr
	}
	return append([]byte(nil), s.value...), nil
}

func (s *memoryStore) Put(_ context.Context, _ string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.value = append([]byte(nil), value...)
	return nil
}

func TestResolverReadsAndCachesCentralBundle(t *testing.T) {
	store := &memoryStore{value: []byte(`{"schema_version":1,"credentials":{"minimax":{"api_key":"example-key"}}}`)}
	resolver, err := NewResolver(store, "eventiapp/prod/ai-provider-credentials", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		key, err := resolver.APIKey(context.Background(), "minimax")
		if err != nil || key != "example-key" {
			t.Fatalf("key=%q err=%v", key, err)
		}
	}
	if store.gets != 1 {
		t.Fatalf("central bundle reads = %d, want 1", store.gets)
	}
}

func TestResolverReplacesOneProviderWithoutLosingOthers(t *testing.T) {
	store := &memoryStore{value: []byte(`{"schema_version":1,"credentials":{"minimax":{"api_key":"old"},"openrouter":{"api_key":"keep"}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.ReplaceAPIKey(context.Background(), "minimax", "new"); err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(store.value)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Credentials["minimax"].APIKey != "new" || bundle.Credentials["openrouter"].APIKey != "keep" {
		t.Fatalf("unexpected bundle: %#v", bundle.Credentials)
	}
	if store.puts != 1 {
		t.Fatalf("writes = %d, want 1", store.puts)
	}
}

func TestResolverRejectsUnknownFieldsAndNeverLeaksStoreErrors(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":1,"credentials":{"minimax":{"api_key":"key","unexpected":true}}}`)); err == nil {
		t.Fatal("unknown credential field accepted")
	}
	store := &memoryStore{getErr: errors.New("access denied for arn:secret:example")}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.APIKey(context.Background(), "minimax")
	if err == nil || err.Error() != "AI provider credential bundle is unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}
