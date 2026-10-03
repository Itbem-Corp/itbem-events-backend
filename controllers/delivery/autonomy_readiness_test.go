package delivery

import (
	"encoding/json"
	"events-stocks/internal/deliverypolicy"
	"strings"
	"testing"
	"time"
)

func TestAutonomyReadinessDoesNotConfusePolicyWithOperationalAutonomy(t *testing.T) {
	policy := deliverypolicy.ResolvedPolicy{Resolved: true, Mode: deliverypolicy.ModeRelease, GateApprovalMode: deliverypolicy.GateApprovalDelegated, Digest: strings.Repeat("a", 64)}
	result := describeAutonomyReadiness(policy, true, true, time.Now())
	if result.State != "blocked" {
		t.Fatal("unimplemented coordinator must block activation")
	}
	checks := projectPreparation{Checks: result.Checks}
	if preparationCheckByKey(checks, "policy").State != "ready" || preparationCheckByKey(checks, "github_protections").State != "unknown" || preparationCheckByKey(checks, "release_environment").State != "unknown" {
		t.Fatal("local configuration must not imply live qualification")
	}
	encoded, _ := json.Marshal(result)
	for _, private := range []string{"private_key", "installation_id", "approved_by", "token"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private field %s leaked", private)
		}
	}
}

func TestAutonomyReadinessNamesMissingConfigurationAndHumanPolicy(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		result := describeAutonomyReadiness(deliverypolicy.ResolvedPolicy{Resolved: resolved, Mode: deliverypolicy.ModeMerge, GateApprovalMode: deliverypolicy.GateApprovalHuman}, false, false, time.Now())
		checks := projectPreparation{Checks: result.Checks}
		for _, key := range []string{"policy", "source_app", "publication_app"} {
			if preparationCheckByKey(checks, key).State != "missing" {
				t.Errorf("missing requirement %s was hidden", key)
			}
		}
	}
}
