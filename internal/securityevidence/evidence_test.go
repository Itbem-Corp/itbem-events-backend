package securityevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func validObservation() Observation {
	return Observation{
		SchemaVersion: SchemaVersion, TaskID: "11111111-1111-4111-8111-111111111111", MatrixDigest: strings.Repeat("a", 64),
		Repositories: []Repository{
			{Reference: "workspace://web", Branch: "itbem-agent/22222222-2222-4222-8222-222222222222", SecretScanPassed: true},
			{Reference: "workspace://api", Branch: "itbem-agent/11111111-1111-4111-8111-111111111111", SecretScanPassed: true, HighFindings: 1},
		},
	}
}

func TestCanonicalSecurityObservationIsStable(t *testing.T) {
	canonical, err := Canonical(validObservation())
	if err != nil || canonical.Repositories[0].Reference != "workspace://api" {
		t.Fatalf("security observation was not canonicalized: %#v / %v", canonical, err)
	}
}

func TestDecodeSecurityObservationFailsClosed(t *testing.T) {
	valid, _ := json.Marshal(validObservation())
	if _, err := Decode(valid); err != nil {
		t.Fatalf("valid security observation was rejected: %v", err)
	}
	for _, payload := range [][]byte{
		append(valid, []byte(` {}`)...),
		[]byte(`{"schema_version":1,"task_id":"11111111-1111-4111-8111-111111111111","matrix_digest":"` + strings.Repeat("a", 64) + `","repositories":[],"override":true}`),
	} {
		if _, err := Decode(payload); err == nil {
			t.Fatalf("unsafe security observation was accepted: %s", payload)
		}
	}
	invalid := validObservation()
	invalid.Repositories[1].Reference = invalid.Repositories[0].Reference
	if err := Validate(invalid); err == nil {
		t.Fatal("duplicate security repository was accepted")
	}
	invalid = validObservation()
	invalid.Repositories[0].CriticalFindings = MaxFindings + 1
	if err := Validate(invalid); err == nil {
		t.Fatal("unbounded security findings were accepted")
	}
}
