package delivery

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"events-stocks/internal/automationagent"
	"events-stocks/internal/projectvault"
	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestLoadOnboardingGitHubAppConfigUsesDedicatedReadOnlySourceApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	values := map[string]string{
		"ITBEM_GITHUB_APP_ID":                  "11111",
		"ITBEM_GITHUB_INSTALLATION_IDS":        "11111",
		"ITBEM_GITHUB_APP_PRIVATE_KEY":         string(encoded),
		"ITBEM_GITHUB_SOURCE_APP_ID":           "22222",
		"ITBEM_GITHUB_SOURCE_INSTALLATION_IDS": "22222",
		"ITBEM_GITHUB_SOURCE_APP_PRIVATE_KEY":  string(encoded),
	}
	config, err := loadOnboardingGitHubAppConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.AppID != "22222" || !config.AllowsInstallationID(22222) || config.AllowsInstallationID(11111) {
		t.Fatalf("onboarding must use only the read-only Source App configuration, got %#v", config)
	}
	if _, err := automationagent.LoadGitHubSourceAppConfig(func(name string) string {
		if strings.HasPrefix(name, "ITBEM_GITHUB_SOURCE_") {
			return ""
		}
		return values[name]
	}); err == nil {
		t.Fatal("publication configuration unexpectedly satisfied Source App loading")
	}
}

func TestCapabilityProbeTaskViewNeverExposesPrivateTaskMetadata(t *testing.T) {
	now := time.Now().UTC()
	task := models.AutomationTask{
		ID: uuid.Must(uuid.NewV4()), Status: "failed", AttemptCount: 2, CompletedAt: &now, CreatedAt: now,
		InputRef: "s3://private-input/secret.json", OutputRef: "s3://private-output/evidence.json",
		RunID: "qa-host-private", LeaseExpiresAt: &now, ErrorMessage: "internal path and command failure",
	}
	encoded, err := json.Marshal(capabilityProbeTaskView(task))
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, forbidden := range []string{"s3://", "private", "lease", "error_message", "input_ref", "output_ref"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("safe capability probe task projection leaked %q: %s", forbidden, value)
		}
	}
	if !strings.Contains(value, `"status":"failed"`) || !strings.Contains(value, `"attempt_count":2`) {
		t.Fatalf("safe projection omitted public task state: %s", value)
	}
}

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
