package qaevidence

import (
	"strings"
	"testing"
)

func TestJSONIntegrityRejectsAmbiguousObjects(t *testing.T) {
	for _, decode := range []func([]byte) error{
		func(raw []byte) error { _, err := Decode(raw); return err },
		func(raw []byte) error { _, err := DecodeClaims(raw); return err },
	} {
		err := decode([]byte(`{"preview_passed":false,"preview_passed":true}`))
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatal("public decoder did not reject ambiguity before schema validation")
		}
	}
	for _, input := range []string{
		`{"passed":false,"passed":true}`,
		`{"passed":false,"PASSED":true}`,
		`{"passed":false,"p\u0061ssed":true}`,
		`{"commands":[{"index":0,"index":1}]}`,
		`{"outer":{"passed":false,"passed":true}}`,
		`{}`, // Two documents, even when each is individually valid.
	} {
		if input == `{}` {
			input += ` {}`
		}
		if err := validateJSONIntegrity([]byte(input)); err == nil {
			t.Fatalf("ambiguous JSON accepted: %s", input)
		}
	}
	for _, input := range []string{`{"passed":false}`, `[{"passed":false},{"passed":true}]`, `{"first":{"index":0},"second":{"index":0}}`} {
		if err := validateJSONIntegrity([]byte(input)); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateJSONIntegrity([]byte(strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66))); err == nil {
		t.Fatal("unbounded nesting accepted")
	}
}
