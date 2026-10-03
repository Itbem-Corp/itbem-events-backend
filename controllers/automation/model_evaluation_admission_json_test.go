package automation

import (
	"strings"
	"testing"

	"events-stocks/internal/modelevaluation"
)

func TestEvaluationAdmissionRejectsAmbiguousAndOversizedJSON(t *testing.T) {
	valid := `{"id":"00000000-0000-0000-0000-000000000001","corpus_version":"` + modelevaluation.CorpusVersion + `"}`
	request, err := decodeEvaluationAdmission(strings.NewReader(valid))
	if err != nil || request.CorpusVersion != modelevaluation.CorpusVersion {
		t.Fatalf("valid bounded admission rejected: %#v / %v", request, err)
	}
	for _, body := range []string{
		strings.Replace(valid, `"id":`, `"id":"overridden","id":`, 1),
		strings.Replace(valid, `"corpus_version":`, `"CORPUS_VERSION":"overridden","corpus_version":`, 1),
		strings.Replace(valid, `"corpus_version":`, `"corpus_versio\u006e":"overridden","corpus_version":`, 1),
		valid + strings.Repeat(" ", 2049), valid + `{}`, valid + string([]byte{0xff}),
		strings.Replace(valid, `"id":`, `"prompt":"override","id":`, 1),
	} {
		if _, err := decodeEvaluationAdmission(strings.NewReader(body)); err == nil {
			t.Fatal("ambiguous or oversized evaluation admission accepted")
		}
	}
}
