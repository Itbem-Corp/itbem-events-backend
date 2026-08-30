package models

import (
	"encoding/json"
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestDeliveryRepositoryOnboardingSerializesStructuredJSON(t *testing.T) {
	value := DeliveryRepositoryOnboarding{ProposalJSON: `{"schema_version":1,"capabilities":[{"name":"source","state":"ready"}]}`}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if json.Unmarshal(encoded, &payload) != nil {
		t.Fatalf("invalid response JSON: %s", encoded)
	}
	if _, ok := payload["proposal"].(map[string]any); !ok {
		t.Fatalf("proposal is not an object: %#v", payload["proposal"])
	}
	if _, ok := payload["capability_matrix"].([]any); !ok {
		t.Fatalf("capability matrix is not an array: %#v", payload["capability_matrix"])
	}
}

func TestDeliveryVaultRevisionFailsClosedAndIsImmutable(t *testing.T) {
	encoded, err := json.Marshal(DeliveryProjectVaultRevision{ManifestJSON: `"not-an-object"`})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	if manifest, ok := payload["manifest"].(map[string]any); !ok || len(manifest) != 0 {
		t.Fatalf("malformed manifest did not fail closed: %#v", payload["manifest"])
	}
	value := &DeliveryProjectVaultRevision{}
	if !errors.Is(value.BeforeUpdate(&gorm.DB{}), ErrImmutableVaultRevision) || !errors.Is(value.BeforeDelete(&gorm.DB{}), ErrImmutableVaultRevision) {
		t.Fatal("Vault revision hooks must reject mutation")
	}
}
