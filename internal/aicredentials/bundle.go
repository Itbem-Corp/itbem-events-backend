// Package aicredentials owns the small, versioned credential bundle used by
// the cloud inference gateway. It deliberately exposes credentials only to
// in-process callers; HTTP handlers must never serialize this package's values.
package aicredentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	SchemaVersion      = 1
	defaultCacheTTL    = 5 * time.Minute
	maxBundleBytes     = 64 << 10
	maxProviderEntries = 16
	maxAPIKeyBytes     = 8 << 10
)

var providerName = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// Bundle is the full SecretString stored in AWS Secrets Manager. Keep routing,
// models, budgets, account IDs and other non-secret metadata in PostgreSQL;
// this document is intentionally limited to provider authentication material.
type Bundle struct {
	SchemaVersion int                           `json:"schema_version"`
	Credentials   map[string]ProviderCredential `json:"credentials"`
}

type ProviderCredential struct {
	APIKey string `json:"api_key"`
}

func (b Bundle) Validate() error {
	if b.SchemaVersion != SchemaVersion {
		return errors.New("AI credential bundle schema is unsupported")
	}
	if len(b.Credentials) > maxProviderEntries {
		return errors.New("AI credential bundle provider count is invalid")
	}
	for provider, credential := range b.Credentials {
		if !providerName.MatchString(provider) {
			return errors.New("AI credential bundle provider identifier is invalid")
		}
		if key := strings.TrimSpace(credential.APIKey); key == "" || len(key) > maxAPIKeyBytes {
			return errors.New("AI credential bundle contains an invalid provider credential")
		}
	}
	return nil
}

func Parse(raw []byte) (Bundle, error) {
	if len(raw) == 0 || len(raw) > maxBundleBytes {
		return Bundle{}, errors.New("AI credential bundle size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, errors.New("AI credential bundle is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Bundle{}, errors.New("AI credential bundle is not valid JSON")
	}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (b Bundle) Marshal() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	// json.Marshal sorts map keys, keeping secret versions deterministic and
	// making audit-only version comparisons stable without retaining a key.
	return json.Marshal(b)
}

// Store is deliberately tiny so all parsing, cache and redaction guarantees
// can be unit tested without AWS. Implementations must not log raw values.
type Store interface {
	Get(context.Context, string) ([]byte, error)
	Put(context.Context, string, []byte) error
}

type Resolver struct {
	store    Store
	secretID string
	ttl      time.Duration
	now      func() time.Time

	mu        sync.RWMutex
	updateMu  sync.Mutex
	cached    Bundle
	expiresAt time.Time
}

func NewResolver(store Store, secretID string, ttl time.Duration) (*Resolver, error) {
	if store == nil || strings.TrimSpace(secretID) == "" {
		return nil, errors.New("AI credential resolver configuration is required")
	}
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &Resolver{store: store, secretID: strings.TrimSpace(secretID), ttl: ttl, now: time.Now}, nil
}

// APIKey returns a key only to the cloud gateway call path. Callers must not
// log it, persist it, place it in an error, or return it from a handler.
func (r *Resolver) APIKey(ctx context.Context, provider string) (string, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if !providerName.MatchString(provider) {
		return "", errors.New("AI provider is invalid")
	}
	bundle, err := r.bundle(ctx, false)
	if err != nil {
		return "", err
	}
	credential, ok := bundle.Credentials[provider]
	if !ok {
		return "", errors.New("AI provider credential is not configured")
	}
	return credential.APIKey, nil
}

// ReplaceAPIKey is called only by the privileged Settings mutation path. A
// bundle update is atomic in Secrets Manager, but a multi-instance API must
// also serialize this method with its durable configuration transaction before
// enabling concurrent dashboard writers.
func (r *Resolver) ReplaceAPIKey(ctx context.Context, provider, apiKey string) error {
	provider = strings.TrimSpace(strings.ToLower(provider))
	apiKey = strings.TrimSpace(apiKey)
	if !providerName.MatchString(provider) || apiKey == "" || len(apiKey) > maxAPIKeyBytes {
		return errors.New("AI provider credential is invalid")
	}
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	bundle, err := r.bundle(ctx, true)
	if err != nil {
		return err
	}
	bundle.Credentials[provider] = ProviderCredential{APIKey: apiKey}
	raw, err := bundle.Marshal()
	if err != nil {
		return err
	}
	if err := r.store.Put(ctx, r.secretID, raw); err != nil {
		return errors.New("AI provider credential could not be stored")
	}
	r.mu.Lock()
	r.cached, r.expiresAt = Bundle{}, time.Time{}
	r.mu.Unlock()
	return nil
}

func (r *Resolver) bundle(ctx context.Context, force bool) (Bundle, error) {
	now := r.now().UTC()
	if !force {
		r.mu.RLock()
		cached, expiresAt := r.cached, r.expiresAt
		r.mu.RUnlock()
		if !expiresAt.IsZero() && now.Before(expiresAt) {
			return clone(cached), nil
		}
	}
	raw, err := r.store.Get(ctx, r.secretID)
	if err != nil {
		return Bundle{}, errors.New("AI provider credential bundle is unavailable")
	}
	bundle, err := Parse(raw)
	if err != nil {
		return Bundle{}, err
	}
	r.mu.Lock()
	r.cached, r.expiresAt = clone(bundle), now.Add(r.ttl)
	r.mu.Unlock()
	return bundle, nil
}

func clone(source Bundle) Bundle {
	result := Bundle{SchemaVersion: source.SchemaVersion, Credentials: make(map[string]ProviderCredential, len(source.Credentials))}
	keys := make([]string, 0, len(source.Credentials))
	for key := range source.Credentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result.Credentials[key] = source.Credentials[key]
	}
	return result
}

// ErrNotConfigured lets endpoints fail closed without revealing whether an
// individual provider was present in the central bundle.
var ErrNotConfigured = fmt.Errorf("AI provider credential is not configured")
