package qaevidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGroundingRequiresCompleteObservedResults(t *testing.T) {
	task := "a4a4b837-2e18-43af-9f58-6d59629db2bb"
	observation := Observation{SchemaVersion: 2, TaskID: task, MatrixDigest: strings.Repeat("a", 64), PreviewPassed: true, RepositoryExecutionOrder: []string{"workspace://repo"}, Repositories: []Repository{{Reference: "workspace://repo", Branch: "itbem-agent/" + task, Commands: []Command{{Index: 0, Phase: "validation", Kind: "unit", Passed: true}, {Index: 1, Phase: "qa", Kind: "security", Passed: false}}}}}
	baseline := `{"schema_version":1,"task_id":"` + task + `","matrix_digest":"` + strings.Repeat("a", 64) + `","preview_passed":true,"verdict":"failed","commands":[{"reference":"workspace://repo","index":0,"phase":"validation","kind":"unit","passed":true},{"reference":"workspace://repo","index":1,"phase":"qa","kind":"security","passed":false}]}`
	cases := []struct {
		name   string
		mutate func(*Claims)
		valid  bool
	}{
		{"correct failed results", func(*Claims) {}, true},
		{"reordered claims", func(c *Claims) { c.Commands[0], c.Commands[1] = c.Commands[1], c.Commands[0] }, true},
		{"other task", func(c *Claims) { c.TaskID = "other" }, false},
		{"other matrix", func(c *Claims) { c.MatrixDigest = strings.Repeat("b", 64) }, false},
		{"missing preview", func(c *Claims) { c.PreviewPassed = nil }, false},
		{"incorrect preview", func(c *Claims) { v := false; c.PreviewPassed = &v }, false},
		{"false success", func(c *Claims) { c.Verdict = "passed" }, false},
		{"omitted failure", func(c *Claims) { c.Commands = c.Commands[:1] }, false},
		{"invented repository", func(c *Claims) { c.Commands[0].Reference = "workspace://invented" }, false},
		{"invented index", func(c *Claims) { v := 7; c.Commands[0].Index = &v }, false},
		{"duplicate claim", func(c *Claims) { c.Commands[1] = c.Commands[0] }, false},
		{"wrong phase", func(c *Claims) { c.Commands[0].Phase = "qa" }, false},
		{"wrong test identity", func(c *Claims) { c.Commands[0].Kind = "integration" }, false},
		{"wrong result", func(c *Claims) { v := true; c.Commands[1].Passed = &v }, false},
		{"missing result", func(c *Claims) { c.Commands[1].Passed = nil }, false},
		{"missing zero index", func(c *Claims) { c.Commands[0].Index = nil }, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			claims, err := DecodeClaims([]byte(baseline))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&claims)
			if (ValidateGrounding(observation, claims) == nil) != test.valid {
				t.Fatal("incorrect grounded verdict")
			}
		})
	}
	claims, err := DecodeClaims([]byte(baseline))
	if err != nil {
		t.Fatal(err)
	}
	observation.Repositories[0].Commands[1].Passed = true
	claims.Verdict = "passed"
	v := true
	claims.Commands[1].Passed = &v
	if err := ValidateGrounding(observation, claims); err != nil {
		t.Fatal(err)
	}
	observation.PreviewPassed = false
	previewFailed := false
	claims.PreviewPassed = &previewFailed
	claims.Verdict = "failed"
	if err := ValidateGrounding(observation, claims); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{}`, `null`, `{"schema_version":1,"unknown":true}`, baseline + ` {}`, strings.Replace(baseline, `"passed":false`, `"passed":"false"`, 1)} {
		decoded, err := DecodeClaims([]byte(payload))
		if err == nil && ValidateGrounding(observation, decoded) == nil {
			t.Fatal("malformed claims accepted")
		}
	}
	raw, _ := json.Marshal(claims)
	if _, err := DecodeClaims(raw); err != nil {
		t.Fatal(err)
	}
}
