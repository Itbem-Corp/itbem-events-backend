package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScoreRetainsGroundedAndFailedEvidence(t *testing.T) {
	root := t.TempDir()
	observation := filepath.Join(root, "observation.json")
	claims := filepath.Join(root, "claims.json")
	output := filepath.Join(root, "score.json")
	task := "a4a4b837-2e18-43af-9f58-6d59629db2bb"
	digest := strings.Repeat("a", 64)
	observed := `{"schema_version":2,"task_id":"` + task + `","matrix_digest":"` + digest + `","preview_passed":true,"repository_execution_order":["workspace://repo"],"repositories":[{"reference":"workspace://repo","branch":"itbem-agent/` + task + `","commands":[{"index":0,"phase":"qa","kind":"unit","passed":false}]}]}`
	valid := `{"schema_version":1,"task_id":"` + task + `","matrix_digest":"` + digest + `","preview_passed":true,"verdict":"failed","commands":[{"reference":"workspace://repo","index":0,"phase":"qa","kind":"unit","passed":false}]}`
	if err := os.WriteFile(observation, []byte(observed), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, input string
		passed      bool
	}{
		{"grounded", valid, true},
		{"invented", strings.Replace(valid, "workspace://repo", "workspace://invented", 1), false},
		{"false-success", strings.Replace(valid, `"verdict":"failed"`, `"verdict":"passed"`, 1), false},
		{"malformed", "not-json", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(claims, []byte(test.input), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			exit := run([]string{"-observation", observation, "-claims", claims, "-output", output}, &stdout, &stderr)
			if (exit == 0) != test.passed {
				t.Fatalf("exit %d: %s", exit, stderr.String())
			}
			retained, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(retained, stdout.Bytes()) {
				t.Fatal("retained verdict differs")
			}
			var score struct {
				Passed          bool     `json:"passed"`
				ObservationHash string   `json:"observation_sha256"`
				ClaimsHash      string   `json:"claims_sha256"`
				Errors          []string `json:"errors"`
			}
			if err := json.Unmarshal(retained, &score); err != nil {
				t.Fatal(err)
			}
			if score.Passed != test.passed || len(score.ObservationHash) != 64 || len(score.ClaimsHash) != 64 || (len(score.Errors) == 0) != test.passed {
				t.Fatal("incorrect evidence")
			}
		})
	}
	var stdout, stderr bytes.Buffer
	if run([]string{"-observation", observation, "-claims", filepath.Join(root, "missing")}, &stdout, &stderr) != 1 {
		t.Fatal("missing claims accepted")
	}
	if run([]string{}, &stdout, &stderr) != 2 {
		t.Fatal("missing flags accepted")
	}
	if err := os.WriteFile(claims, bytes.Repeat([]byte("x"), 65537), 0600); err != nil {
		t.Fatal(err)
	}
	if run([]string{"-observation", observation, "-claims", claims}, &stdout, &stderr) != 1 {
		t.Fatal("oversize input accepted")
	}
}
