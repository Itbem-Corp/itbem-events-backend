package automation

import (
	"context"
	"log/slog"
	"time"

	"events-stocks/internal/automationagent"
)

const defaultProviderCatalogSyncInterval = 7 * 24 * time.Hour

var cataloguedProviders = []automationagent.Provider{
	automationagent.ProviderMiniMax,
	automationagent.ProviderDeepSeek,
	automationagent.ProviderOpenRouter,
	automationagent.ProviderOpenAI,
	automationagent.ProviderAnthropic,
	automationagent.ProviderOpenCodeGo,
}

// StartProviderCatalogSynchronizer refreshes the credential-free model
// projection in the background. It is intentionally best-effort: a provider
// outage must never delay API startup or make a previously stored catalogue
// unavailable. An immediate pass makes a deployment current; subsequent passes
// use the configured cadence (seven days by default).
func StartProviderCatalogSynchronizer(ctx context.Context, configuredHours int) {
	if ctx == nil || inferenceCredentials == nil {
		return
	}
	interval := providerCatalogSyncInterval(configuredHours)
	go func() {
		synchronizeProviderCatalogues(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				synchronizeProviderCatalogues(ctx)
			}
		}
	}()
}

func providerCatalogSyncInterval(configuredHours int) time.Duration {
	if configuredHours < 1 || configuredHours > 24*31 {
		return defaultProviderCatalogSyncInterval
	}
	return time.Duration(configuredHours) * time.Hour
}

func synchronizeProviderCatalogues(parent context.Context) {
	metadataContext, cancelMetadata := context.WithTimeout(parent, 30*time.Second)
	metadata, metadataErr := automationagent.FetchProviderModelMetadata(metadataContext, nil)
	cancelMetadata()
	if metadataErr != nil {
		slog.Warn("public model metadata synchronization failed")
	}
	for _, provider := range cataloguedProviders {
		if parent.Err() != nil {
			return
		}
		apiKey, err := inferenceCredentials.APIKey(parent, string(provider))
		if err != nil {
			// The public catalog remains useful for provider comparison, but its
			// models stay non-selectable until this account is authenticated.
			if metadata != nil {
				catalogue := automationagent.PublicProviderModels(provider, metadata)
				if len(catalogue) > 0 {
					snapshot := recordProviderCatalogSnapshot(provider, catalogue)
					slog.Info("public provider catalogue synchronized", "provider", provider, "model_count", len(catalogue), "changed", snapshot.Changed, "catalog_hash", snapshot.CatalogHash)
				}
			}
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		catalogue, err := automationagent.ListProviderModelsWithMetadata(ctx, provider, apiKey, nil, metadata)
		cancel()
		if err != nil {
			slog.Warn("provider catalogue synchronization failed", "provider", provider)
			continue
		}
		_, snapshot := recordCredentialSafeProviderCatalogSnapshot(provider, apiKey, catalogue)
		slog.Info("provider catalogue synchronized", "provider", provider, "model_count", len(catalogue), "changed", snapshot.Changed, "catalog_hash", snapshot.CatalogHash)
	}
}
