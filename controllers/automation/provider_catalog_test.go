package automation

import (
	"testing"

	"events-stocks/internal/automationagent"
)

func TestDiffProviderCataloguesReportsMeaningfulModelChanges(t *testing.T) {
	previous := []automationagent.ProviderModel{
		{ID: "kept", InputMicrosPerMillion: 100, OutputMicrosPerMillion: 200, PricingKnown: true, ContextWindowTokens: 1000, Supported: true, Availability: "account_verified"},
		{ID: "removed"},
	}
	current := []automationagent.ProviderModel{
		{ID: "kept", InputMicrosPerMillion: 150, OutputMicrosPerMillion: 200, PricingKnown: true, ContextWindowTokens: 2000, Supported: false, Availability: "gateway_incompatible"},
		{ID: "added"},
	}
	changes := diffProviderCatalogues("openrouter", previous, current)
	want := map[string]bool{"added:added": false, "removed:removed": false, "pricing_changed:kept": false, "capabilities_changed:kept": false, "availability_changed:kept": false}
	for _, change := range changes {
		key := change.Kind + ":" + change.Model
		if _, ok := want[key]; ok {
			want[key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Fatalf("expected %s in %#v", key, changes)
		}
	}
}
