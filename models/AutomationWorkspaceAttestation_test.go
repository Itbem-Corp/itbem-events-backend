package models

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestAutomationWorkspaceAttestationUsesStableGitHubRepositoryColumn(t *testing.T) {
	parsed, err := schema.Parse(&AutomationWorkspaceAttestation{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse workspace attestation schema: %v", err)
	}
	field := parsed.LookUpField("GitHubRepository")
	if field == nil {
		t.Fatal("GitHubRepository field is missing from the schema")
	}
	if field.DBName != "github_repository" {
		t.Fatalf("GitHubRepository column = %q, want github_repository", field.DBName)
	}
}
