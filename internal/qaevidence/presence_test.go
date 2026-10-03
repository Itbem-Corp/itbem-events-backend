package qaevidence

import (
	"encoding/json"
	"testing"
)

func TestObservationRequiresExplicitZeroAndFalseResults(t *testing.T) {
	observation := validObservation()
	observation.PreviewPassed = false
	observation.Repositories[0].Commands = observation.Repositories[0].Commands[1:]
	observation.Repositories[0].Commands[0].Passed = false
	baseline, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(baseline); err != nil {
		t.Fatal("explicit false and zero rejected", err)
	}
	for _, field := range []string{"preview_passed", "index", "passed"} {
		for _, missing := range []bool{true, false} {
			var document map[string]any
			if err := json.Unmarshal(baseline, &document); err != nil {
				t.Fatal(err)
			}
			target := document
			if field != "preview_passed" {
				target = document["repositories"].([]any)[0].(map[string]any)["commands"].([]any)[0].(map[string]any)
			}
			if missing {
				delete(target, field)
			} else {
				target[field] = nil
			}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(raw); err == nil {
				t.Fatalf("missing/null %s was accepted as an observed value", field)
			}
		}
	}
}
