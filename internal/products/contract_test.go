package products

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPinnedProductContractMatchesBackendRegistry(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", ".contracts", "itbem-product-contract", "contract", "products.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		SchemaVersion int `json:"schemaVersion"`
		Products      []struct {
			Code     string `json:"code"`
			Identity struct {
				Name         string `json:"name"`
				ProductLabel string `json:"productLabel"`
			} `json:"identity"`
			Capabilities struct {
				AllowsPlatformAuthority bool `json:"allowsPlatformAuthority"`
				SupportsEventOperations bool `json:"supportsEventOperations"`
				SupportsAutomation      bool `json:"supportsAutomation"`
			} `json:"capabilities"`
			Modules []string `json:"modules"`
		} `json:"products"`
	}
	if err := json.Unmarshal(payload, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.SchemaVersion != 1 {
		t.Fatalf("unsupported product contract version %d", contract.SchemaVersion)
	}
	if len(contract.Products) != len(All()) {
		t.Fatalf("contract has %d products; registry has %d", len(contract.Products), len(All()))
	}
	for _, external := range contract.Products {
		local, ok := Resolve(external.Code)
		if !ok ||
			local.Name != external.Identity.Name ||
			local.ProductLabel != external.Identity.ProductLabel ||
			!slices.Equal(local.Modules, external.Modules) ||
			local.AllowsPlatformAuthority != external.Capabilities.AllowsPlatformAuthority ||
			local.SupportsEventOperations != external.Capabilities.SupportsEventOperations ||
			local.SupportsAutomation != external.Capabilities.SupportsAutomation {
			t.Fatalf("backend registry diverged for product %q", external.Code)
		}
	}
}
