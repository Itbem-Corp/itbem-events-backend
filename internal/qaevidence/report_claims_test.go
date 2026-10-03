package qaevidence

import (
	"os"
	"strings"
	"testing"
)

func TestDecodeReportClaimsPreservesModelAssertions(t *testing.T) {
	observedRaw, err := os.ReadFile("testdata/grounding/observation.json")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := Decode(observedRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []string{"grounded", "invented"} {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/grounding/" + fixture + "-claims.json")
			if err != nil {
				t.Fatal(err)
			}
			report := `{"summary":"Narrative text","verdict":"failed","claims":` + string(raw) + `}`
			claims, err := DecodeReportClaims([]byte(report))
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateGrounding(observation, claims)
			if (err == nil) != (fixture == "grounded") {
				t.Fatalf("model assertions changed during extraction: %v", err)
			}
		})
	}
}

func TestDecodeReportClaimsRejectsAmbiguousEnvelope(t *testing.T) {
	raw, err := os.ReadFile("testdata/grounding/grounded-claims.json")
	if err != nil {
		t.Fatal(err)
	}
	claims := string(raw)
	for _, report := range []string{
		`{}`, `{"claims":null}`, `{"claims":[]}`, `null`, `[]`,
		`{"claims":` + claims + `,"claims":` + claims + `}`,
		`{"claims":` + claims + `,"CLAIMS":` + claims + `}`,
		`{"claims":` + claims + `,"\u0063laims":` + claims + `}`,
		`{"summary":"first","summary":"second","claims":` + claims + `}`,
		`{"claims":` + strings.Replace(claims, `"passed": false`, `"passed": true, "pa\u017f\u017fed": false`, 1) + `}`,
		`{"claims":` + claims + `} {}`,
		strings.Repeat(" ", maxInputBytes) + `{}`,
	} {
		if _, err := DecodeReportClaims([]byte(report)); err == nil {
			t.Fatalf("ambiguous or absent claims accepted: %.120s", report)
		}
	}
}
