package aicredentials

import (
	"context"
	"errors"
	"strings"
	"time"
)

type SecretsStoreFactory func(context.Context, string) (Store, error)

// NewResolverForEnvironment makes the trust boundary explicit: a local API
// process can use only an ignored test file, while every deployed environment
// can use only the single Secrets Manager bundle. A nil resolver means the
// optional inference gateway is disabled.
func NewResolverForEnvironment(ctx context.Context, environment, region, secretID, localFile string, factory SecretsStoreFactory) (*Resolver, error) {
	secretID = strings.TrimSpace(secretID)
	localFile = strings.TrimSpace(localFile)
	if isLocalEnvironment(environment) {
		if secretID != "" {
			return nil, errors.New("AI_PROVIDER_CREDENTIALS_SECRET_ID is not permitted in local development; use AI_PROVIDER_CREDENTIALS_LOCAL_FILE")
		}
		if localFile == "" {
			return nil, nil
		}
		store, err := NewLocalFileStore(localFile)
		if err != nil {
			return nil, err
		}
		return NewResolver(store, "local-file", time.Minute)
	}
	if localFile != "" {
		return nil, errors.New("AI_PROVIDER_CREDENTIALS_LOCAL_FILE is permitted only in local development")
	}
	if secretID == "" {
		return nil, nil
	}
	if factory == nil {
		factory = func(ctx context.Context, region string) (Store, error) { return NewSecretsManagerStore(ctx, region) }
	}
	store, err := factory(ctx, region)
	if err != nil {
		return nil, errors.New("AI provider credential store could not be configured")
	}
	return NewResolver(store, secretID, 5*time.Minute)
}

func IsLocalEnvironment(environment string) bool { return isLocalEnvironment(environment) }

func isLocalEnvironment(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "", "local", "development", "dev", "test":
		return true
	default:
		return false
	}
}
