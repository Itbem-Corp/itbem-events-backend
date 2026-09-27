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

type delayedFirstReadStore struct {
	mu          sync.Mutex
	value       []byte
	reads       int
	firstRead   chan struct{}
	releaseRead chan struct{}
}

func (s *delayedFirstReadStore) Get(_ context.Context, _ string) ([]byte, error) {
	s.mu.Lock()
	s.reads++
	readNumber := s.reads
	value := append([]byte(nil), s.value...)
	s.mu.Unlock()
	if readNumber == 1 {
		close(s.firstRead)
		<-s.releaseRead
	}
	return value, nil
}

func (s *delayedFirstReadStore) Put(_ context.Context, _ string, value []byte) error {
	s.mu.Lock()
	s.value = append([]byte(nil), value...)
	s.mu.Unlock()
	return nil
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

func TestReadRacingWithCredentialRotationCannotRestoreStaleCache(t *testing.T) {
	store := &delayedFirstReadStore{
		value:       []byte(`{"schema_version":2,"credentials":{"minimax":{"api_key":"old"}}}`),
		firstRead:   make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		key string
		err error
	}
	readResult := make(chan result, 1)
	go func() {
		key, err := resolver.APIKey(context.Background(), "minimax")
		readResult <- result{key: key, err: err}
	}()
	<-store.firstRead
	if err := resolver.ReplaceAPIKey(context.Background(), "minimax", "new"); err != nil {
		t.Fatal(err)
	}
	close(store.releaseRead)
	select {
	case got := <-readResult:
		if got.err != nil || got.key != "new" {
			t.Fatal("overlapping pre-rotation read was not retried against the new bundle")
		}
	case <-time.After(time.Second):
		t.Fatal("overlapping credential read did not finish")
	}
	key, err := resolver.APIKey(context.Background(), "minimax")
	if err != nil || key != "new" {
		t.Fatal("pre-rotation bundle repopulated the resolver cache")
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

func TestReplaceInferenceKeyPreservesSeparateUsageCredential(t *testing.T) {
	store := &memoryStore{value: []byte(`{"schema_version":2,"credentials":{"opencode-go":{"api_key":"old-inference","usage_api_key":"separate-usage"}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.ReplaceAPIKey(context.Background(), "opencode-go", "new-inference"); err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(store.value)
	if err != nil {
		t.Fatal(err)
	}
	credential := bundle.Credentials["opencode-go"]
	if credential.APIKey != "new-inference" || credential.UsageAPIKey != "separate-usage" {
		t.Fatal("rotating inference credential unexpectedly changed the usage credential")
	}
}

func TestProjectCredentialsAreIsolatedAndNeverFallBackToGlobalKeys(t *testing.T) {
	projectA := "18a34b7d-3b57-4d61-b525-1394355f8601"
	projectB := "28a34b7d-3b57-4d61-b525-1394355f8602"
	store := &memoryStore{value: []byte(`{"schema_version":2,"credentials":{"minimax":{"api_key":"legacy-global"}},"projects":{"` + projectA + `":{"credentials":{"minimax":{"api_key":"project-a-key"}}},"` + projectB + `":{"credentials":{"openrouter":{"api_key":"project-b-key"}}}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if key, err := resolver.APIKeyForProject(context.Background(), projectA, "minimax"); err != nil || key != "project-a-key" {
		t.Fatalf("project A key=%q err=%v", key, err)
	}
	if key, err := resolver.APIKeyForProject(context.Background(), projectB, "minimax"); err == nil || key != "" {
		t.Fatalf("project B unexpectedly inherited a global key: %q / %v", key, err)
	}
	if key, err := resolver.APIKey(context.Background(), "minimax"); err != nil || key != "legacy-global" {
		t.Fatalf("legacy namespace key=%q err=%v", key, err)
	}
	if configured, err := resolver.HasAPIKeyForProject(context.Background(), projectB, "openrouter"); err != nil || !configured {
		t.Fatalf("credential-free project status=%v err=%v", configured, err)
	}
}

func TestProjectResolverReturnsOnlyExactProjectProviderCredential(t *testing.T) {
	projectA := "18a34b7d-3b57-4d61-b525-1394355f8601"
	projectB := "28a34b7d-3b57-4d61-b525-1394355f8602"
	projectC := "38a34b7d-3b57-4d61-b525-1394355f8603"
	store := &memoryStore{value: []byte(`{"schema_version":2,"credentials":{"openrouter":{"api_key":"global-canary"}},"projects":{"` + projectA + `":{"credentials":{"openrouter":{"api_key":"project-a-canary"}}},"` + projectB + `":{"credentials":{"openrouter":{"api_key":"project-b-canary"}}}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		projectID string
		want      string
	}{
		{projectID: projectA, want: "project-a-canary"},
		{projectID: projectB, want: "project-b-canary"},
	} {
		got, err := resolver.APIKeyForProject(context.Background(), test.projectID, "openrouter")
		if err != nil || got != test.want {
			t.Fatalf("project %s got a different project's/provider's credential: %q / %v", test.projectID, got, err)
		}
	}
	if got, err := resolver.APIKeyForProject(context.Background(), projectC, "openrouter"); err == nil || got != "" {
		t.Fatalf("unconfigured project inherited another scope's key: %q / %v", got, err)
	}
	if got, err := resolver.APIKeyForProject(context.Background(), projectA, "deepseek"); err == nil || got != "" {
		t.Fatalf("unconfigured provider inherited another provider's key: %q / %v", got, err)
	}
}

func TestReplaceProjectProviderCredentialPreservesOtherScopesAndMigratesSchema(t *testing.T) {
	projectA := "18a34b7d-3b57-4d61-b525-1394355f8601"
	projectB := "28a34b7d-3b57-4d61-b525-1394355f8602"
	store := &memoryStore{value: []byte(`{"schema_version":1,"credentials":{"minimax":{"api_key":"legacy-key"}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.ReplaceProjectAPIKey(context.Background(), projectA, "deepseek", "project-a-deepseek"); err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(store.value)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.SchemaVersion != SchemaVersion || bundle.Credentials["minimax"].APIKey != "legacy-key" || bundle.Projects[projectA].Credentials["deepseek"].APIKey != "project-a-deepseek" || len(bundle.Projects) != 1 {
		t.Fatalf("project update did not preserve/migrate the document: %#v", bundle)
	}
	if _, err := resolver.APIKeyForProject(context.Background(), projectB, "minimax"); err == nil {
		t.Fatal("unconfigured project inherited a legacy credential")
	}
	if store.puts != 1 {
		t.Fatalf("writes=%d, want 1", store.puts)
	}
}

func TestDeleteProjectCredentialIsScopedAndIdempotent(t *testing.T) {
	projectA := "18a34b7d-3b57-4d61-b525-1394355f8601"
	projectB := "28a34b7d-3b57-4d61-b525-1394355f8602"
	store := &memoryStore{value: []byte(`{"schema_version":2,"credentials":{"minimax":{"api_key":"global"}},"projects":{"` + projectA + `":{"credentials":{"minimax":{"api_key":"project-a"}}},"` + projectB + `":{"credentials":{"minimax":{"api_key":"project-b"}}}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.DeleteProjectAPIKey(context.Background(), projectA, "minimax"); err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(store.value)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := bundle.Projects[projectA].Credentials["minimax"]; exists || bundle.Projects[projectB].Credentials["minimax"].APIKey != "project-b" || bundle.Credentials["minimax"].APIKey != "global" {
		t.Fatalf("project credential removal escaped its scope: %#v", bundle)
	}
	if err := resolver.DeleteProjectAPIKey(context.Background(), projectA, "minimax"); err != nil {
		t.Fatalf("idempotent removal returned error: %v", err)
	}
}

func TestDeleteLastProjectCredentialRemovesEmptyScope(t *testing.T) {
	projectID := "18a34b7d-3b57-4d61-b525-1394355f8601"
	store := &memoryStore{value: []byte(`{"schema_version":2,"projects":{"` + projectID + `":{"credentials":{"minimax":{"api_key":"project-key"}}}}}`)}
	resolver, err := NewResolver(store, "bundle", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.DeleteProjectAPIKey(context.Background(), projectID, "minimax"); err != nil {
		t.Fatal(err)
	}
	bundle, err := Parse(store.value)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Projects) != 0 {
		t.Fatal("deleting the last project credential left a stale project scope")
	}
}

func TestBundleRejectsInvalidProjectScopeKeys(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":2,"projects":{"../other":{"credentials":{"minimax":{"api_key":"key"}}}}}`)); err == nil {
		t.Fatal("non-UUID project scope accepted")
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

func TestBundleRejectsDuplicateJSONKeysAtEveryScope(t *testing.T) {
	projectID := "18a34b7d-3b57-4d61-b525-1394355f8601"
	cases := map[string]string{
		"top-level property":   `{"schema_version":2,"schema_version":2}`,
		"case-folded property": `{"schema_version":2,"SCHEMA_VERSION":2}`,
		"provider key":         `{"schema_version":1,"credentials":{"minimax":{"api_key":"one"},"minimax":{"api_key":"two"}}}`,
		"credential field":     `{"schema_version":1,"credentials":{"minimax":{"api_key":"one","api_key":"two"}}}`,
		"escaped property":     `{"schema_version":2,"projects":{},"projec\u0074s":{}}`,
		"project scope":        `{"schema_version":2,"projects":{"` + projectID + `":{"credentials":{}},"` + projectID + `":{"credentials":{}}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatal("duplicate JSON key was accepted")
			}
		})
	}
}
