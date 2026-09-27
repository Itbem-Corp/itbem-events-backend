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
	SchemaVersion       = 2
	LegacySchemaVersion = 1
	defaultCacheTTL     = 5 * time.Minute
	maxBundleBytes      = 64 << 10
	maxProviderEntries  = 16
	maxProjectScopes    = 256
	maxAPIKeyBytes      = 8 << 10
)

var providerName = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
var projectIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

// Bundle is the full SecretString stored in AWS Secrets Manager. Keep routing,
// models, budgets, account IDs and other non-secret metadata in PostgreSQL;
// this document is intentionally limited to provider authentication material.
type Bundle struct {
	SchemaVersion int `json:"schema_version"`
	// Credentials is the legacy/platform-wide namespace. New project work must
	// use Projects so a secret stored here cannot silently become a credential
	// fallback for every customer's work.
	Credentials map[string]ProviderCredential `json:"credentials,omitempty"`
	Projects    map[string]ProjectScope       `json:"projects,omitempty"`
}

type ProjectScope struct {
	Credentials map[string]ProviderCredential `json:"credentials"`
}

type ProviderCredential struct {
	APIKey string `json:"api_key"`
	// UsageAPIKey is optional and intentionally separate from APIKey. Some
	// providers (currently OpenCode Console) require a service-account key for
	// read-only usage exports, not an inference key.
	UsageAPIKey string `json:"usage_api_key,omitempty"`
}

func (b Bundle) Validate() error {
	if b.SchemaVersion != LegacySchemaVersion && b.SchemaVersion != SchemaVersion {
		return errors.New("AI credential bundle schema is unsupported")
	}
	if err := validateCredentials(b.Credentials); err != nil {
		return err
	}
	if len(b.Projects) > maxProjectScopes {
		return errors.New("AI credential bundle provider count is invalid")
	}
	for projectID, scope := range b.Projects {
		if !projectIDPattern.MatchString(projectID) {
			return errors.New("AI credential bundle project identifier is invalid")
		}
		if err := validateCredentials(scope.Credentials); err != nil {
			return err
		}
	}
	if b.SchemaVersion == LegacySchemaVersion && len(b.Projects) > 0 {
		return errors.New("AI credential bundle project scopes require schema version 2")
	}
	return nil
}

func validateCredentials(credentials map[string]ProviderCredential) error {
	if len(credentials) > maxProviderEntries {
		return errors.New("AI credential bundle provider count is invalid")
	}
	for provider, credential := range credentials {
		if !providerName.MatchString(provider) {
			return errors.New("AI credential bundle provider identifier is invalid")
		}
		if key := strings.TrimSpace(credential.APIKey); key == "" || len(key) > maxAPIKeyBytes {
			return errors.New("AI credential bundle contains an invalid provider credential")
		}
		if key := strings.TrimSpace(credential.UsageAPIKey); key != "" && len(key) > maxAPIKeyBytes {
			return errors.New("AI credential bundle contains an invalid provider usage credential")
		}
	}
	return nil
}

// UsageAPIKey returns an optional read-only telemetry credential. It is never
// used by inference and must never leave the backend process.
func (r *Resolver) UsageAPIKey(ctx context.Context, provider string) (string, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if !providerName.MatchString(provider) {
		return "", errors.New("AI provider is invalid")
	}
	bundle, err := r.bundle(ctx, false)
	if err != nil {
		return "", err
	}
	credential, ok := bundle.Credentials[provider]
	if !ok || strings.TrimSpace(credential.UsageAPIKey) == "" {
		return "", errors.New("AI provider usage credential is not configured")
	}
	return credential.UsageAPIKey, nil
}

func Parse(raw []byte) (Bundle, error) {
	if len(raw) == 0 || len(raw) > maxBundleBytes {
		return Bundle{}, errors.New("AI credential bundle size is invalid")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return Bundle{}, errors.New("AI credential bundle is not valid JSON")
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

// rejectDuplicateJSONKeys prevents ambiguous configuration documents. The
// standard encoding/json decoder accepts duplicate object keys and silently
// keeps the last value, which can make a reviewed project/provider scope
// differ from the one the resolver actually uses. Compare decoded,
// case-folded keys so escaped equivalents and struct-field casing variants
// cannot select a different last value.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is invalid")
				}
				foldedKey := strings.ToLower(key)
				if _, exists := seen[foldedKey]; exists {
					return errors.New("JSON object contains a duplicate key")
				}
				seen[foldedKey] = struct{}{}
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("JSON object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("JSON array is incomplete")
			}
		default:
			return errors.New("JSON value is invalid")
		}
		return nil
	}
	if err := readValue(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing content")
	}
	return nil
}

// ReplaceUsageAPIKey rotates an optional provider telemetry credential while
// preserving the inference credential in the same atomic bundle update.
func (r *Resolver) ReplaceUsageAPIKey(ctx context.Context, provider, apiKey string) error {
	provider = strings.TrimSpace(strings.ToLower(provider))
	apiKey = strings.TrimSpace(apiKey)
	if !providerName.MatchString(provider) || apiKey == "" || len(apiKey) > maxAPIKeyBytes {
		return errors.New("AI provider usage credential is invalid")
	}
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	bundle, err := r.bundle(ctx, true)
	if err != nil {
		return err
	}
	credential, ok := bundle.Credentials[provider]
	if !ok {
		return errors.New("AI provider credential is not configured")
	}
	credential.UsageAPIKey = apiKey
	bundle.Credentials[provider] = credential
	raw, err := bundle.Marshal()
	if err != nil {
		return err
	}
	if err := r.store.Put(ctx, r.secretID, raw); err != nil {
		return errors.New("AI provider usage credential could not be stored")
	}
	r.invalidateCache()
	return nil
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
	epoch     uint64
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

// APIKeyForProject returns a credential only from the named project's JSON
// section. It deliberately does not fall back to the legacy/global section:
// the resolver reads the whole central bundle, while authorization is enforced
// by the cloud task's persisted project relationship.
func (r *Resolver) APIKeyForProject(ctx context.Context, projectID, provider string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	provider = strings.TrimSpace(strings.ToLower(provider))
	if !projectIDPattern.MatchString(projectID) || !providerName.MatchString(provider) {
		return "", errors.New("AI project provider credential is not configured")
	}
	bundle, err := r.bundle(ctx, false)
	if err != nil {
		return "", err
	}
	scope, ok := bundle.Projects[projectID]
	if !ok {
		return "", errors.New("AI project provider credential is not configured")
	}
	credential, ok := scope.Credentials[provider]
	if !ok || strings.TrimSpace(credential.APIKey) == "" {
		return "", errors.New("AI project provider credential is not configured")
	}
	return credential.APIKey, nil
}

// HasAPIKeyForProject is a credential-free projection for Settings screens.
func (r *Resolver) HasAPIKeyForProject(ctx context.Context, projectID, provider string) (bool, error) {
	projectID = strings.TrimSpace(projectID)
	provider = strings.TrimSpace(strings.ToLower(provider))
	if !projectIDPattern.MatchString(projectID) || !providerName.MatchString(provider) {
		return false, errors.New("AI project provider credential is not configured")
	}
	bundle, err := r.bundle(ctx, false)
	if err != nil {
		return false, err
	}
	credential, ok := bundle.Projects[projectID].Credentials[provider]
	return ok && strings.TrimSpace(credential.APIKey) != "", nil
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
	credential := bundle.Credentials[provider]
	credential.APIKey = apiKey
	bundle.Credentials[provider] = credential
	bundle.SchemaVersion = SchemaVersion
	raw, err := bundle.Marshal()
	if err != nil {
		return err
	}
	if err := r.store.Put(ctx, r.secretID, raw); err != nil {
		return errors.New("AI provider credential could not be stored")
	}
	r.invalidateCache()
	return nil
}

// ReplaceProjectAPIKey updates only one project's provider credential while
// preserving every other section in the central Secrets Manager document.
func (r *Resolver) ReplaceProjectAPIKey(ctx context.Context, projectID, provider, apiKey string) error {
	projectID = strings.TrimSpace(projectID)
	provider = strings.TrimSpace(strings.ToLower(provider))
	apiKey = strings.TrimSpace(apiKey)
	if !projectIDPattern.MatchString(projectID) || !providerName.MatchString(provider) || apiKey == "" || len(apiKey) > maxAPIKeyBytes {
		return errors.New("AI project provider credential is invalid")
	}
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	bundle, err := r.bundle(ctx, true)
	if err != nil {
		return err
	}
	if bundle.Projects == nil {
		bundle.Projects = make(map[string]ProjectScope)
	}
	scope := bundle.Projects[projectID]
	if scope.Credentials == nil {
		scope.Credentials = make(map[string]ProviderCredential)
	}
	credential := scope.Credentials[provider]
	credential.APIKey = apiKey
	scope.Credentials[provider] = credential
	bundle.Projects[projectID] = scope
	bundle.SchemaVersion = SchemaVersion
	raw, err := bundle.Marshal()
	if err != nil {
		return err
	}
	if err := r.store.Put(ctx, r.secretID, raw); err != nil {
		return errors.New("AI project provider credential could not be stored")
	}
	r.invalidateCache()
	return nil
}

// DeleteProjectAPIKey removes one project/provider entry without exposing or
// rewriting other project scopes. Absence is an idempotent success.
func (r *Resolver) DeleteProjectAPIKey(ctx context.Context, projectID, provider string) error {
	projectID = strings.TrimSpace(projectID)
	provider = strings.TrimSpace(strings.ToLower(provider))
	if !projectIDPattern.MatchString(projectID) || !providerName.MatchString(provider) {
		return errors.New("AI project provider credential is invalid")
	}
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	bundle, err := r.bundle(ctx, true)
	if err != nil {
		return err
	}
	scope, exists := bundle.Projects[projectID]
	if !exists {
		return nil
	}
	if _, exists := scope.Credentials[provider]; !exists {
		return nil
	}
	delete(scope.Credentials, provider)
	if len(scope.Credentials) == 0 {
		delete(bundle.Projects, projectID)
	} else {
		bundle.Projects[projectID] = scope
	}
	bundle.SchemaVersion = SchemaVersion
	raw, err := bundle.Marshal()
	if err != nil {
		return err
	}
	if err := r.store.Put(ctx, r.secretID, raw); err != nil {
		return errors.New("AI project provider credential could not be removed")
	}
	r.invalidateCache()
	return nil
}

func (r *Resolver) bundle(ctx context.Context, force bool) (Bundle, error) {
	for {
		now := r.now().UTC()
		r.mu.RLock()
		cached, expiresAt, epoch := r.cached, r.expiresAt, r.epoch
		r.mu.RUnlock()
		if !force && !expiresAt.IsZero() && now.Before(expiresAt) {
			return clone(cached), nil
		}
		raw, err := r.store.Get(ctx, r.secretID)
		if err != nil {
			r.mu.RLock()
			changed := r.epoch != epoch
			r.mu.RUnlock()
			if changed {
				continue
			}
			return Bundle{}, errors.New("AI provider credential bundle is unavailable")
		}
		bundle, err := Parse(raw)
		if err != nil {
			r.mu.RLock()
			changed := r.epoch != epoch
			r.mu.RUnlock()
			if changed {
				continue
			}
			return Bundle{}, err
		}
		r.mu.Lock()
		if r.epoch != epoch {
			r.mu.Unlock()
			continue
		}
		r.cached, r.expiresAt = clone(bundle), now.Add(r.ttl)
		r.mu.Unlock()
		return bundle, nil
	}
}

func (r *Resolver) invalidateCache() {
	r.mu.Lock()
	r.cached, r.expiresAt = Bundle{}, time.Time{}
	r.epoch++
	r.mu.Unlock()
}

func clone(source Bundle) Bundle {
	result := Bundle{SchemaVersion: source.SchemaVersion, Credentials: make(map[string]ProviderCredential, len(source.Credentials)), Projects: make(map[string]ProjectScope, len(source.Projects))}
	keys := make([]string, 0, len(source.Credentials))
	for key := range source.Credentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result.Credentials[key] = source.Credentials[key]
	}
	for projectID, scope := range source.Projects {
		credentials := make(map[string]ProviderCredential, len(scope.Credentials))
		for provider, credential := range scope.Credentials {
			credentials[provider] = credential
		}
		result.Projects[projectID] = ProjectScope{Credentials: credentials}
	}
	return result
}

// ErrNotConfigured lets endpoints fail closed without revealing whether an
// individual provider was present in the central bundle.
var ErrNotConfigured = fmt.Errorf("AI provider credential is not configured")
