package models

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/gofrs/uuid"
)

func TestDeliveryPlanStepDependencyPatchManifestSHA256CanonicalMultiRepository(t *testing.T) {
	dependencyID := uuid.Must(uuid.NewV4()).String()
	first := DeliveryPlanStepDependencyPatchReference{
		DependencyStepID: dependencyID, RepositoryRef: "workspace://repo-a", BaseSHA: strings.Repeat("a", 40),
		SHA256: strings.Repeat("1", 64), SizeBytes: 1024,
	}
	second := DeliveryPlanStepDependencyPatchReference{
		DependencyStepID: dependencyID, RepositoryRef: "workspace://repo-b", BaseSHA: strings.Repeat("b", 64),
		SHA256: strings.Repeat("2", 64), SizeBytes: 2048,
	}
	forward, err := DeliveryPlanStepDependencyPatchManifestSHA256([]DeliveryPlanStepDependencyPatchReference{first, second})
	if err != nil {
		t.Fatalf("manifest digest: %v", err)
	}
	reversed, err := DeliveryPlanStepDependencyPatchManifestSHA256([]DeliveryPlanStepDependencyPatchReference{second, first})
	if err != nil {
		t.Fatalf("reversed manifest digest: %v", err)
	}
	if forward != reversed {
		t.Fatalf("manifest should be order-independent: %s != %s", forward, reversed)
	}
	canonical := strings.Join([]string{
		dependencyID + "\nworkspace://repo-a\n" + strings.Repeat("a", 40) + "\n" + strings.Repeat("1", 64) + "\n1024\n",
		dependencyID + "\nworkspace://repo-b\n" + strings.Repeat("b", 64) + "\n" + strings.Repeat("2", 64) + "\n2048\n",
	}, "")
	expected := sha256.Sum256([]byte(canonical))
	if forward != hex.EncodeToString(expected[:]) {
		t.Fatalf("unexpected canonical digest: got %s, want %s", forward, hex.EncodeToString(expected[:]))
	}
}

func TestDeliveryPlanStepDependencyPatchManifestSHA256EmptySet(t *testing.T) {
	got, err := DeliveryPlanStepDependencyPatchManifestSHA256(nil)
	if err != nil {
		t.Fatalf("empty manifest digest: %v", err)
	}
	empty := sha256.Sum256(nil)
	if got != hex.EncodeToString(empty[:]) {
		t.Fatalf("empty manifest digest = %s, want %s", got, hex.EncodeToString(empty[:]))
	}
}

func TestDeliveryPlanStepDependencyPatchManifestSHA256RejectsDuplicateRepository(t *testing.T) {
	reference := DeliveryPlanStepDependencyPatchReference{
		DependencyStepID: uuid.Must(uuid.NewV4()).String(), RepositoryRef: "workspace://repo-a", BaseSHA: strings.Repeat("a", 40),
		SHA256: strings.Repeat("1", 64), SizeBytes: 16,
	}
	if _, err := DeliveryPlanStepDependencyPatchManifestSHA256([]DeliveryPlanStepDependencyPatchReference{reference, reference}); err == nil {
		t.Fatal("expected duplicate dependency/repository to be rejected")
	}
}
