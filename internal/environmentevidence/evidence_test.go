package environmentevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func validObservation() Observation {
	return Observation{
		SchemaVersion: SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64),
		Repositories: []Repository{{
			Repository: "example/service", HeadSHA: strings.Repeat("b", 40), Workflow: ".github/workflows/deploy.yml", Environment: "production",
			RequiredSecretReferences: []string{"DATABASE_URL"}, RequiredVariableReferences: []string{}, WorkflowExists: true, EnvironmentExists: true,
			MissingSecretReferences: []string{}, MissingVariableReferences: []string{},
		}},
	}
}

func TestCanonicalEnvironmentObservationIsStableAndValueFree(t *testing.T) {
	observation := validObservation()
	canonical, err := Canonical(observation)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !decoded.Repositories[0].Ready() {
		t.Fatalf("canonical observation did not round trip: %#v / %v", decoded, err)
	}
	for _, forbidden := range []string{"value", "token", "command", "output", "inventory"} {
		if strings.Contains(strings.ToLower(string(encoded)), `"`+forbidden+`"`) {
			t.Fatalf("observation exposed forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestEnvironmentObservationRejectsUnapprovedMissingNamesAndPartialReads(t *testing.T) {
	for name, mutate := range map[string]func(*Observation){
		"unapproved missing name":  func(value *Observation) { value.Repositories[0].MissingSecretReferences = []string{"OTHER_SECRET"} },
		"mutable revision":         func(value *Observation) { value.Repositories[0].HeadSHA = "main" },
		"partial environment read": func(value *Observation) { value.Repositories[0].EnvironmentExists = false },
		"nil explicit references":  func(value *Observation) { value.Repositories[0].RequiredVariableReferences = nil },
	} {
		t.Run(name, func(t *testing.T) {
			value := validObservation()
			mutate(&value)
			if _, err := Canonical(value); err == nil {
				t.Fatalf("unsafe environment observation was accepted: %#v", value)
			}
		})
	}
}
