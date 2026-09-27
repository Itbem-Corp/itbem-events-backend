package automation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
)

func TestProviderCatalogSyncIntervalUsesWeeklyDefaultAndBoundedOverride(t *testing.T) {
	if got := providerCatalogSyncInterval(0); got != 7*24*time.Hour {
		t.Fatalf("zero interval = %s, want weekly default", got)
	}
	if got := providerCatalogSyncInterval(168); got != 7*24*time.Hour {
		t.Fatalf("weekly override = %s, want 168h", got)
	}
	if got := providerCatalogSyncInterval(2); got != 2*time.Hour {
		t.Fatalf("custom interval = %s, want 2h", got)
	}
	if got := providerCatalogSyncInterval(745); got != 7*24*time.Hour {
		t.Fatalf("oversized interval = %s, want weekly default", got)
	}
}

func TestProviderCatalogSyncRedactsCredentialEchoBeforeSnapshotAndExposure(t *testing.T) {
	const secret = "provider-catalog-secret-canary"
	previousDB := configuration.DB
	configuration.DB = nil
	t.Cleanup(func() { configuration.DB = previousDB })
	upstreamCatalogue := []automationagent.ProviderModel{{
		ID: "model-" + secret, Name: secret, Description: "Provider echoed " + secret,
		SupportedParameters: []string{secret}, Variants: []automationagent.ProviderModelVariant{{ID: secret, Name: secret}},
	}}
	safeCatalogue, snapshot := recordCredentialSafeProviderCatalogSnapshot(automationagent.ProviderOpenRouter, secret, upstreamCatalogue)
	encoded, err := json.Marshal(safeCatalogue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || snapshot.ModelCount != 1 || snapshot.CatalogHash == "" {
		t.Fatalf("catalogue boundary did not redact before snapshot/exposure: models=%s snapshot=%#v", encoded, snapshot)
	}
	if upstreamCatalogue[0].Name != secret {
		t.Fatal("catalogue sanitization mutated the provider response in place")
	}
}
