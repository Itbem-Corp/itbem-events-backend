package requestcontext

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProjectionMatchesPinnedContract(t *testing.T) {
	raw, err := os.ReadFile("../../.contracts/itbem-product-contract/contract/request-context.v1.json")
	if err != nil {
		t.Fatalf("read pinned request context contract: %v", err)
	}
	var contract struct {
		SchemaVersion int `json:"schemaVersion"`
		Headers       struct {
			ApplicationCode     string `json:"applicationCode"`
			WorkspaceMode       string `json:"workspaceMode"`
			OrganizationID      string `json:"organizationId"`
			OrganizationContext string `json:"organizationContext"`
		} `json:"headers"`
		WorkspaceModes []string `json:"workspaceModes"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode pinned request context contract: %v", err)
	}
	if contract.SchemaVersion != 1 ||
		contract.Headers.ApplicationCode != HeaderApplicationCode ||
		contract.Headers.WorkspaceMode != HeaderWorkspaceMode ||
		contract.Headers.OrganizationID != HeaderOrganizationID ||
		contract.Headers.OrganizationContext != HeaderOrganizationContext {
		t.Fatalf("backend request context projection diverges from pinned contract")
	}
	modes := map[string]bool{}
	for _, mode := range contract.WorkspaceModes {
		modes[mode] = true
	}
	if !modes[WorkspaceOrganization] || !modes[WorkspacePlatform] {
		t.Fatalf("backend workspace modes diverge from pinned contract")
	}
}
