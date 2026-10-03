package evidencejson

import (
	"strings"
	"testing"
)

func TestValidateEvidenceJSON(t *testing.T) {
	for _, input := range []string{
		`{"decision":"approved","decision":"rejected"}`,
		`{"decision":"approved","DECISION":"rejected"}`,
		`{"passed":true,"pa\u017f\u017fed":false}`,
		`{"kind":"plan","\u212aind":"release"}`,
		`{"claims":[{"index":0,"\u0069ndex":1}]}`,
		`{} {}`, `{"claims":`,
		strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
	} {
		if err := Validate([]byte(input)); err == nil {
			t.Fatalf("ambiguous input accepted: %s", input)
		}
	}
	if err := Validate([]byte(`{"claims":[{"index":0},{"index":1}],"passed":false}`)); err != nil {
		t.Fatal(err)
	}
}
