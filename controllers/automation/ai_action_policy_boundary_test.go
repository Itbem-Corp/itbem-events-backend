package automation

import (
	"encoding/json"
	"strings"
	"testing"

	"events-stocks/internal/automationagent"
)

func TestProviderModelProjectionRedactsCredentialFromEveryExposedString(t *testing.T) {
	const secret = "provider-model-secret-canary"
	catalogue := []automationagent.ProviderModel{{
		ID: secret, Name: "Model " + secret, Description: "Description " + secret,
		Publisher: secret, Family: secret, ReleaseDate: secret, LastUpdated: secret, KnowledgeCutoff: secret,
		PricingSource: secret, GatewayAPI: secret, Availability: secret, Source: secret,
		InputModalities: []string{secret}, OutputModalities: []string{secret}, SupportedParameters: []string{secret},
		CapabilityTags: []string{secret}, ReasoningEfforts: []string{secret},
		Variants:     []automationagent.ProviderModelVariant{{ID: secret, Name: secret}},
		PricingTiers: []automationagent.ProviderModelPricingTier{{Kind: secret}},
	}}
	redacted := redactProviderCatalogCredential(catalogue, secret)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("provider key escaped through the public model projection: %s", encoded)
	}
	if catalogue[0].ID != secret {
		t.Fatal("redaction must not mutate the source catalogue")
	}
}
