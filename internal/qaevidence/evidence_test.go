package qaevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func validObservation() Observation {
	return Observation{
		SchemaVersion: SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64), PreviewPassed: true,
		RepositoryExecutionOrder: []string{"workspace://api", "workspace://web"},
		Repositories: []Repository{
			{Reference: "workspace://web", Branch: "itbem-agent/22222222-2222-4222-8222-222222222222", Commands: []Command{{Index: 1, Phase: "qa", Kind: "e2e", Passed: true}, {Index: 0, Phase: "validation", Kind: "unit", Passed: true}}},
			{Reference: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", Commands: []Command{}},
		},
	}
}

func TestCanonicalQAObservationIsStableWithoutChangingExecutionOrder(t *testing.T) {
	observation := validObservation()
	canonical, err := Canonical(observation)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Repositories[0].Reference != "workspace://api" || canonical.Repositories[1].Commands[0].Index != 0 || canonical.RepositoryExecutionOrder[0] != "workspace://api" {
		t.Fatalf("QA observation was not canonicalized safely: %#v", canonical)
	}
}

func TestDecodeQAObservationFailsClosed(t *testing.T) {
	valid, _ := json.Marshal(validObservation())
	if _, err := Decode(valid); err != nil {
		t.Fatalf("valid QA observation was rejected: %v", err)
	}
	for _, payload := range [][]byte{
		append(valid, []byte(` {}`)...),
		[]byte(`{"schema_version":1,"task_id":"11111111-1111-4111-8111-111111111111","matrix_digest":"` + strings.Repeat("a", 64) + `","preview_passed":true,"repository_execution_order":[],"repositories":[],"override":true}`),
	} {
		if _, err := Decode(payload); err == nil {
			t.Fatalf("unsafe QA observation was accepted: %s", payload)
		}
	}
	invalid := validObservation()
	invalid.Repositories[0].Commands = append(invalid.Repositories[0].Commands, Command{Index: 1, Phase: "qa", Passed: true})
	if err := Validate(invalid); err == nil {
		t.Fatal("duplicate QA command evidence was accepted")
	}
	duplicateKind := validObservation()
	duplicateKind.Repositories[0].Commands[1].Kind = "E2E"
	if err := Validate(duplicateKind); err == nil {
		t.Fatal("duplicate QA test identity was accepted for one repository")
	}
}
