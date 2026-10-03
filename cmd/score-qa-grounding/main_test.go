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
		{"duplicate-result", strings.Replace(valid, `"passed":false`, `"passed":true,"passed":false`, 1), false},
		{"case-aliased-result", strings.Replace(valid, `"passed":false`, `"PASSED":true,"passed":false`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := filepath.Join(root, test.name+"-score.json")
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
	if run([]string{"-observation", observation, "-claims", claims, "-output", observation}, &stdout, &stderr) != 2 {
		t.Fatal("score overwrote observation input")
	}
	unchanged, err := os.ReadFile(observation)
	if err != nil || string(unchanged) != observed {
		t.Fatal("observation input was modified")
	}
	if err := os.WriteFile(output, []byte("existing evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if run([]string{"-observation", observation, "-claims", claims, "-output", output}, &stdout, &stderr) != 2 {
		t.Fatal("score overwrote existing evidence")
	}
	unchanged, err = os.ReadFile(output)
	if err != nil || string(unchanged) != "existing evidence" {
		t.Fatal("existing score was modified")
	}
	alias := filepath.Join(root, "observation-alias.json")
	if err := os.Link(observation, alias); err != nil {
		t.Fatal(err)
	}
	if run([]string{"-observation", observation, "-claims", claims, "-output", alias}, &stdout, &stderr) != 2 {
		t.Fatal("score overwrote hard-link input alias")
	}
	unchanged, err = os.ReadFile(observation)
	if err != nil || string(unchanged) != observed {
		t.Fatal("aliased observation input was modified")
	}
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

func TestVersionedGroundingFixtures(t *testing.T) {
	fixture := filepath.Join("..", "..", "internal", "qaevidence", "testdata", "grounding")
	for _, test := range []struct {
		name string
		exit int
	}{{"grounded", 0}, {"invented", 1}} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exit := run([]string{"-observation", filepath.Join(fixture, "observation.json"), "-claims", filepath.Join(fixture, test.name+"-claims.json")}, &stdout, &stderr)
			if exit != test.exit {
				t.Fatalf("exit=%d, stderr=%s", exit, stderr.String())
			}
			var score struct {
				Passed          bool     `json:"passed"`
				Errors          []string `json:"errors"`
				Kind            string   `json:"score_kind"`
				ObservedVerdict string   `json:"observed_qa_verdict"`
				ClaimedVerdict  string   `json:"claimed_qa_verdict"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &score); err != nil {
				t.Fatal(err)
			}
			if score.Passed != (test.exit == 0) {
				t.Fatal("fixture score changed")
			}
			if score.Kind != "structured_qa_grounding" || score.ObservedVerdict != "failed" || score.ClaimedVerdict != "failed" {
				t.Fatal("grounding success confused with observed QA success")
			}
			if test.name == "invented" && (len(score.Errors) != 1 || !strings.Contains(score.Errors[0], "unknown or duplicated")) {
				t.Fatal("invented fixture failed for an unrelated reason")
			}
		})
	}
}
