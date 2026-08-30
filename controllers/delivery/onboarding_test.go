package delivery

import (
	"encoding/json"
	"testing"

	"events-stocks/internal/projectvault"
	"events-stocks/models"
)

func TestValidateStoredOnboardingProposalPinsDigestAndCheckpoint(t *testing.T) {
	proposal, err := projectvault.Build(projectvault.Input{
		Repository: projectvault.Repository{Reference: "github://acme/service", DefaultBranch: "trunk", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files:      []string{"go.mod", ".github/workflows/ci.yml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(proposal)
	proposalDigest, _ := projectvault.ProposalSHA256(proposal)
	onboarding := models.DeliveryRepositoryOnboarding{RepositoryReference: proposal.Repository.Reference, DefaultBranch: proposal.Repository.DefaultBranch, Revision: proposal.Repository.Revision, Readiness: proposal.Readiness, ProposalJSON: string(encoded), ProposalSHA256: proposalDigest, VaultSHA256: proposal.VaultSHA256}
	if _, err := validateStoredOnboardingProposal(onboarding); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	onboarding.Revision = "ffffffffffffffffffffffffffffffffffffffff"
	if _, err := validateStoredOnboardingProposal(onboarding); err == nil {
		t.Fatal("mismatched approval checkpoint accepted")
	}
}

func TestValidateStoredOnboardingProposalRejectsTamperedVault(t *testing.T) {
	proposal, err := projectvault.Build(projectvault.Input{
		Repository: projectvault.Repository{Reference: "github://acme/service", DefaultBranch: "main", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files:      []string{"go.mod"},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal.Vault.Entries[0].Value["default_branch"] = "attacker"
	encoded, _ := json.Marshal(proposal)
	proposalDigest, _ := projectvault.ProposalSHA256(proposal)
	onboarding := models.DeliveryRepositoryOnboarding{RepositoryReference: proposal.Repository.Reference, DefaultBranch: proposal.Repository.DefaultBranch, Revision: proposal.Repository.Revision, Readiness: proposal.Readiness, ProposalJSON: string(encoded), ProposalSHA256: proposalDigest, VaultSHA256: proposal.VaultSHA256}
	if _, err := validateStoredOnboardingProposal(onboarding); err == nil {
		t.Fatal("tampered Vault accepted with stale digest")
	}
}

func TestValidateStoredOnboardingProposalRejectsCapabilityElevation(t *testing.T) {
	proposal, err := projectvault.Build(projectvault.Input{
		Repository: projectvault.Repository{Reference: "github://acme/service", DefaultBranch: "main", Revision: "0123456789abcdef0123456789abcdef01234567"},
		Files:      []string{"go.mod"},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposalDigest, _ := projectvault.ProposalSHA256(proposal)
	proposal.Capabilities[10].State = "ready"
	encoded, _ := json.Marshal(proposal)
	onboarding := models.DeliveryRepositoryOnboarding{
		RepositoryReference: proposal.Repository.Reference, DefaultBranch: proposal.Repository.DefaultBranch,
		Revision: proposal.Repository.Revision, Readiness: proposal.Readiness, ProposalJSON: string(encoded),
		ProposalSHA256: proposalDigest, VaultSHA256: proposal.VaultSHA256,
	}
	if _, err := validateStoredOnboardingProposal(onboarding); err == nil {
		t.Fatal("capability elevation accepted with stale proposal digest")
	}
}
