package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"fmt"
	"strings"
	"testing"
)

func TestProviderForRuntimeOmitsModelCredentialOnlyForReleaseWorker(t *testing.T) {
	release := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease}}
	provider, name, model, err := providerForRuntime(release, func(string) string { return "" })
	if err != nil || provider == nil || name != "" || model != "" {
		t.Fatalf("release provider = %T %q %q %v", provider, name, model, err)
	}
	if _, err := provider.Complete(context.Background(), nil, 0); err == nil {
		t.Fatal("deterministic release provider allowed a model call")
	}

	reviewer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview}}
	if provider, _, _, err := providerForRuntime(reviewer, func(string) string { return "" }); err == nil || provider != nil {
		t.Fatalf("reviewer started without provider credential: %T %v", provider, err)
	}
}

func TestDoctorReportFailsClosedWithoutExecutionDependenciesAndNeverLeaksValues(t *testing.T) {
	report, ready, err := doctorReport(func(key string) string {
		if key == "MINIMAX_API_KEY" || key == "AUTOMATION_CALLBACK_SECRET" {
			return "must-never-appear"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if ready || report["ready"] != false || report["workspaces_ready"] != false {
		t.Fatalf("missing workspace/runtime configuration must fail closed: %#v", report)
	}
	provider, ok := report["provider"].(map[string]any)
	if !ok || provider["ready"] != true || provider["status"] != "configured_unverified" {
		t.Fatalf("a configured provider should be reported without exposing its key: %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-never-appear") {
		t.Fatalf("doctor output leaked a configured secret: %s", raw)
	}
}

func TestDoctorReviewIngressNeverLeaksConfigurationAndReportsIncompleteSetup(t *testing.T) {
	status := doctorReviewIngress(func(key string) string {
		if key == "GITHUB_REVIEW_WEBHOOK_SECRET" {
			return "must-never-appear"
		}
		if key == "GITHUB_REVIEW_REPOSITORIES" {
			return "itbem/backend"
		}
		return ""
	}, false, true)
	if status["enabled"] != true || status["ready"] != false || status["status"] != "incomplete" || status["allowed_repository_count"] != 1 {
		t.Fatalf("incomplete ingress should be explicit: %#v", status)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "must-never-appear") || strings.Contains(string(raw), "itbem/backend") {
		t.Fatalf("doctor review ingress leaked secret or repository identity: %s", raw)
	}
}

func TestDoctorPublicationReadinessRequiresRoleSpecificGitHubAppConfiguration(t *testing.T) {
	release := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReleaseManager, Lane: agentwork.LaneRelease}}
	reviewer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview}}
	engineer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}}
	if doctorExecutionReady(true, true, true, false, release) {
		t.Fatal("release doctor passed without publication identity")
	}
	if !doctorExecutionReady(true, true, true, true, release) {
		t.Fatal("release doctor rejected a complete deterministic runtime")
	}
	if doctorExecutionReady(true, true, true, false, reviewer) {
		t.Fatal("review doctor passed without its independent publication identity")
	}
	if !doctorExecutionReady(true, true, true, true, reviewer) {
		t.Fatal("review doctor rejected a complete independent publication identity")
	}
	if !doctorExecutionReady(true, true, true, false, engineer) {
		t.Fatal("non-publishing engineer doctor made publication identity mandatory")
	}
}

func TestGitHubAuthProbeIsRequiredOnlyForPublishingRolesAndRedactsFailures(t *testing.T) {
	engineer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RolePrincipalEngineer, Lane: agentwork.LaneEngineering}}
	called := false
	report, err := githubAuthProbeReport(context.Background(), engineer, func(string) string {
		t.Fatal("non-publishing probe read GitHub credentials")
		return ""
	}, func(context.Context, automationagent.GitHubAppConfig) error {
		called = true
		return nil
	})
	if err != nil || called || report["ready"] != true || report["status"] != "not_required" || report["network_checks_made"] != false {
		t.Fatalf("non-publishing GitHub probe = %#v, called=%v, err=%v", report, called, err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	reviewer := automationagent.RuntimeConfig{WorkerConfig: automationagent.WorkerConfig{Role: agentwork.RoleReviewer, Lane: agentwork.LaneReview}}
	lookup := func(name string) string {
		values := map[string]string{
			"ITBEM_GITHUB_APP_ID": "12345", "ITBEM_GITHUB_INSTALLATION_IDS": "67890,67891",
			"ITBEM_GITHUB_APP_PRIVATE_KEY": privatePEM,
		}
		return values[name]
	}
	verifiedInstallations := make([]string, 0, 2)
	report, err = githubAuthProbeReport(context.Background(), reviewer, lookup, func(_ context.Context, config automationagent.GitHubAppConfig) error {
		called = true
		if config.AppID != "12345" {
			t.Fatalf("unexpected GitHub App identity: %#v", config)
		}
		verifiedInstallations = append(verifiedInstallations, config.InstallationID)
		return nil
	})
	if err != nil || !called || report["ready"] != true || report["status"] != "authenticated" || report["network_checks_made"] != true || report["installation_count"] != 2 {
		t.Fatalf("reviewer GitHub probe = %#v, called=%v, err=%v", report, called, err)
	}
	if strings.Join(verifiedInstallations, ",") != "67890,67891" {
		t.Fatalf("not every configured installation was verified: %#v", verifiedInstallations)
	}

	_, err = githubAuthProbeReport(context.Background(), reviewer, lookup, func(context.Context, automationagent.GitHubAppConfig) error {
		return fmt.Errorf("must-never-appear")
	})
	if err == nil || strings.Contains(err.Error(), "must-never-appear") {
		t.Fatalf("GitHub probe did not redact remote failure: %v", err)
	}
}
