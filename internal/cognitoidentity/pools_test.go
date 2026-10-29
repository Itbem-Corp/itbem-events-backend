package cognitoidentity

import (
	"events-stocks/models"
	"testing"
)

func TestCanonicalSubjectPreservesLegacyIdentityOnlyWithMatchingUsername(t *testing.T) {
	p := Pool{PoolID: "us-east-2_NewPool"}
	old := "11111111-1111-4111-8111-111111111111"
	sub := "22222222-2222-4222-8222-222222222222"
	got, err := CanonicalSubject(p, sub, old, old)
	if err != nil || got != old {
		t.Fatalf("legacy identity lost: %s %v", got, err)
	}
	if _, err := CanonicalSubject(p, sub, "attacker", old); err == nil {
		t.Fatal("accepted forged legacy binding")
	}
	got, err = CanonicalSubject(p, sub, "new-user", "")
	if err != nil || got != "us-east-2_NewPool|new-user" {
		t.Fatalf("new identity must be pool scoped: %s %v", got, err)
	}
	if _, err := CanonicalSubject(p, sub, "user|injected", ""); err == nil {
		t.Fatal("accepted ambiguous subject")
	}
}

func TestPoolMapRejectsAmbiguousOrUntrustedConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{"unknown":{"poolId":"us-east-2_New","clientId":"client"}}`,
		`{"eventiapp":{"poolId":"https://attacker/","clientId":"client"}}`,
		`{"eventiapp":{"poolId":"us-east-2_New","clientId":"same"},"itbem":{"poolId":"us-east-2_Other","clientId":"same"}}`,
		`{"eventiapp":{"poolId":"us-east-2_New","clientId":"a"},"itbem":{"poolId":"us-east-2_New","clientId":"b"}}`,
	} {
		if _, err := Parse(&models.Config{CognitoTenantPools: raw}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
