package models

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestDeliveryPolicyRevisionProjectsPatchWithoutActorIdentity(t *testing.T) {
	revision := DeliveryPolicyRevision{PatchJSON: `{"mode":"review_only"}`, ProposedBy: "private-proposer-sub"}
	encoded, err := json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	if !strings.Contains(value, `"patch":{"mode":"review_only"}`) || strings.Contains(value, "private-proposer-sub") || strings.Contains(value, "patch_json") {
		t.Fatalf("policy revision projection exposed private or malformed storage data: %s", value)
	}

	revision.PatchJSON = `"not-an-object"`
	encoded, err = json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"patch":{}`) {
		t.Fatalf("malformed policy patch did not fail closed: %s", encoded)
	}
}

func TestDeliveryPolicyLedgersRejectMutation(t *testing.T) {
	revision := &DeliveryPolicyRevision{}
	decision := &DeliveryPolicyDecision{}
	for name, err := range map[string]error{
		"revision update": revision.BeforeUpdate(&gorm.DB{}),
		"revision delete": revision.BeforeDelete(&gorm.DB{}),
		"decision update": decision.BeforeUpdate(&gorm.DB{}),
		"decision delete": decision.BeforeDelete(&gorm.DB{}),
	} {
		if !errors.Is(err, ErrImmutableDeliveryPolicy) {
			t.Fatalf("%s did not reject append-only mutation: %v", name, err)
		}
	}
}

func TestDeliveryPolicySchemaBindsDecisionToExactRevisionDigest(t *testing.T) {
	decisionSchema, err := schema.Parse(&DeliveryPolicyDecision{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	relation := decisionSchema.Relationships.Relations["PolicyRevision"]
	if relation == nil || len(relation.References) != 2 {
		t.Fatalf("policy decision must have a composite revision/digest foreign key: %#v", relation)
	}
	foreignKeys := map[string]bool{}
	for _, reference := range relation.References {
		foreignKeys[reference.ForeignKey.DBName] = true
	}
	if !foreignKeys["policy_revision_id"] || !foreignKeys["policy_digest"] {
		t.Fatalf("policy decision foreign key does not bind the exact digest: %#v", foreignKeys)
	}

	revisionSchema, err := schema.Parse(&DeliveryPolicyRevision{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	checks := revisionSchema.ParseCheckConstraints()
	for _, name := range []string{"chk_delivery_policy_level", "chk_delivery_policy_scope", "chk_delivery_policy_patch", "chk_delivery_policy_revision_digest"} {
		if _, present := checks[name]; !present {
			t.Fatalf("database safety constraint %s is missing", name)
		}
	}
}
