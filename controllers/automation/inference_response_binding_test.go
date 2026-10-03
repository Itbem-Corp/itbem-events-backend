package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestInferenceResponseBindingRetainsExactBytesWithoutAnswerBody(t *testing.T) {
	for _, content := range []string{"", "{\"changes\":[]}\n", " respuesta ñ\n"} {
		binding := inferenceResponseBinding(content)
		digest := sha256.Sum256([]byte(content))
		if binding["sha256"] != hex.EncodeToString(digest[:]) || binding["bytes"] != len([]byte(content)) {
			t.Fatal("response bytes changed")
		}
		raw, err := json.Marshal(map[string]any{"_itbem_response": binding})
		if err != nil {
			t.Fatal(err)
		}
		hash, count := recordedInferenceResponseBinding(string(raw))
		if hash == nil || count == nil || *hash != binding["sha256"] || *count != int64(len([]byte(content))) {
			t.Fatal("binding not retained")
		}
		if content != "" && strings.Contains(string(raw), content) {
			t.Fatal("answer body copied into metadata")
		}
	}
	if inferenceResponseBinding("x")["sha256"] == inferenceResponseBinding("x\n")["sha256"] {
		t.Fatal("trailing newline lost")
	}
	usage := sanitizeProviderUsage(map[string]any{"prompt_tokens": 10, "completion_tokens": 20, "_itbem_response": map[string]any{"sha256": "forged"}})
	if _, ok := usage["_itbem_response"]; ok {
		t.Fatal("provider-controlled binding survived sanitization")
	}
}

func TestHistoricalOrInvalidResponseBindingRemainsUnavailable(t *testing.T) {
	for _, raw := range []string{"{}", "null", "invalid", `{"_itbem_response":{"version":"unknown","sha256":"a","bytes":0}}`,
		`{"_itbem_response":{"version":"utf8-final-answer-v1","sha256":"short","bytes":0}}`,
		`{"_itbem_response":{"version":"utf8-final-answer-v1","sha256":"` + strings.Repeat("g", 64) + `","bytes":0}}`,
		`{"_itbem_response":{"version":"utf8-final-answer-v1","sha256":"` + strings.Repeat("A", 64) + `","bytes":0}}`,
		`{"_itbem_response":{"version":"utf8-final-answer-v1","sha256":"` + strings.Repeat("a", 64) + `"}}`,
		`{"_itbem_response":{"version":"utf8-final-answer-v1","sha256":"` + strings.Repeat("a", 64) + `","bytes":-1}}`} {
		if hash, count := recordedInferenceResponseBinding(raw); hash != nil || count != nil {
			t.Fatalf("invalid binding trusted: %s", raw)
		}
	}
}
