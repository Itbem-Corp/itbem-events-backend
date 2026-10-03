package automationagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDelegatedPublicationRequiresOperatorSecurityChecksAndIsolation(t *testing.T) {
	config := WorkspaceConfig{SandboxRuntime: WorkspaceSandboxDocker, QACommands: [][]string{{"scanner", "secrets"}, {"scanner", "vulnerabilities"}}, QACommandKinds: []string{"security:secrets", "security:high-critical"}}
	checks, err := delegatedPublicationSecurityCommands(config)
	if err != nil || len(checks) != 2 || checks[0].Kind != "security:secrets" {
		t.Fatalf("valid checks rejected: %#v / %v", checks, err)
	}
	for _, bad := range []WorkspaceConfig{
		{}, {SandboxRuntime: WorkspaceSandboxDocker},
		{SandboxRuntime: WorkspaceSandboxDocker, QACommands: config.QACommands, QACommandKinds: []string{"security:secrets", "unit"}},
		{SandboxRuntime: WorkspaceSandboxDocker, QACommands: append(config.QACommands, []string{"scanner"}), QACommandKinds: []string{"security:secrets", "security:high-critical", "security:secrets"}},
	} {
		if _, err := delegatedPublicationSecurityCommands(bad); err == nil {
			t.Fatal("unsafe pre-publication configuration accepted")
		}
	}
}

func TestPublicationAuthorityRenewalRejectsRevocationAndExpiry(t *testing.T) {
	auth := &publicationAuthorization{GateAuthority: "delegated", AllowedTargetBranches: []string{"main"}, GrantID: "00000000-0000-4000-8000-000000000001", RepositoryRef: "workspace://api", BaseSHA: strings.Repeat("a", 40), GitHubRepository: "example/service", ReviewDiffSHA256: strings.Repeat("b", 64), Branch: "itbem-agent/00000000-0000-4000-8000-000000000002", Capabilities: []string{WorkspaceCapabilityStageCommit, WorkspaceCapabilityPublishBranch}, ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if err := refreshPublicationAuthority(context.Background(), auth, func(context.Context) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := refreshPublicationAuthority(context.Background(), auth, nil); err == nil {
		t.Fatal("delegated publication skipped live authority")
	}
	if err := refreshPublicationAuthority(context.Background(), auth, func(context.Context) (bool, error) { return false, nil }); err == nil {
		t.Fatal("revoked authority accepted")
	}
	auth.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
	called := false
	if err := refreshPublicationAuthority(context.Background(), auth, func(context.Context) (bool, error) { called = true; return true, nil }); err == nil || called {
		t.Fatal("expired grant reached renewal")
	}
}

func TestPublicationHandoffRetainsTargetAndSecurityReceipts(t *testing.T) {
	handoff := publicationHandoff(map[string]any{"target_branch": "main", "security_checks": []string{"security:secrets", "security:high-critical"}, "token": "must-not-leak"})
	if handoff["target_branch"] != "main" || handoff["security_checks"] == nil || handoff["token"] != nil {
		t.Fatalf("incorrect public handoff: %#v", handoff)
	}
}
