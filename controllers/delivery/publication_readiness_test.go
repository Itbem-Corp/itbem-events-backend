package delivery

import "testing"

func TestPublicationReadinessFailsClosedWithoutOrWithInvalidGitHubAppConfiguration(t *testing.T) {
	missing := publicationReadinessForEnvironment(func(string) string { return "" })
	if missing.State != "not_configured" || missing.Provider != "github_app" {
		t.Fatalf("missing configuration readiness = %#v", missing)
	}
	if missing.Message == "" {
		t.Fatal("missing configuration must explain the safe local-only posture")
	}
	if len(missing.Requirements) < 3 {
		t.Fatalf("missing configuration must provide safe setup requirements, got %#v", missing.Requirements)
	}

	invalid := publicationReadinessForEnvironment(func(name string) string {
		switch name {
		case "ITBEM_GITHUB_APP_ID":
			return "not-a-number"
		case "ITBEM_GITHUB_INSTALLATION_ID":
			return "42"
		case "ITBEM_GITHUB_APP_PRIVATE_KEY":
			return "not-a-private-key"
		default:
			return ""
		}
	})
	if invalid.State != "invalid" || invalid.Provider != "github_app" {
		t.Fatalf("invalid configuration readiness = %#v", invalid)
	}
	if len(invalid.Requirements) == 0 {
		t.Fatal("invalid configuration must explain how to recover without exposing credentials")
	}
}
