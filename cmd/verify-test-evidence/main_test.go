package main

import (
	"strings"
	"testing"
)

func TestVerifyExecutionEvidence(t *testing.T) {
	const run = `{"Action":"run","Package":"example/pkg","Test":"TestProof"}` + "\n"
	const pass = `{"Action":"pass","Package":"example/pkg","Test":"TestProof"}` + "\n"
	const skip = `{"Action":"skip","Package":"example/pkg","Test":"TestProof"}` + "\n"
	const done = `{"Action":"pass","Package":"example/pkg"}` + "\n"
	for _, tc := range []struct {
		name, log   string
		repetitions int
		allowSkips  bool
		required    []string
		wantPass    bool
	}{
		{"complete", run + pass + run + pass + done, 2, false, []string{"TestProof"}, true},
		{"empty", "", 2, false, nil, false},
		{"no tests", done, 2, false, nil, false},
		{"truncated", run + pass, 1, false, nil, false},
		{"short repetition", run + pass + done, 2, false, nil, false},
		{"duplicate completion", run + pass + done + done, 1, false, nil, false},
		{"premature completion", done + run + pass, 1, false, nil, false},
		{"malformed", run + "not-json\n" + done, 1, false, nil, false},
		{"unknown action", run + `{"Action":"green","Package":"example/pkg"}` + "\n", 1, false, nil, false},
		{"missing named proof", run + pass + done, 1, false, []string{"TestMissing"}, false},
		{"strict omission", run + pass + run + skip + done, 2, false, nil, false},
		{"optional omission", run + pass + run + skip + done, 2, true, nil, true},
		{"required omission", run + pass + run + skip + done, 2, true, []string{"TestProof"}, false},
		{"all omitted", run + skip + done, 1, true, nil, false},
		{"pass without execution", pass + done, 1, false, nil, false},
		{"result before execution with balanced totals", pass + run + done, 1, false, nil, false},
		{"overlapping executions with balanced totals", run + run + pass + pass + done, 2, false, nil, false},
		{"duplicate result before next execution", run + pass + pass + run + done, 2, false, nil, false},
		{"skip before execution with balanced totals", skip + run + run + pass + done, 2, true, nil, false},
		{"fail despite package pass", run + `{"Action":"fail","Package":"example/pkg","Test":"TestProof"}` + "\n" + done, 1, false, nil, false},
		{"wrong package", strings.ReplaceAll(run+pass+done, "example/pkg", "other/pkg"), 1, false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verify(strings.NewReader(tc.log), []string{"example/pkg"}, tc.required, tc.repetitions, tc.allowSkips)
			if (err == nil) != tc.wantPass {
				t.Fatalf("verify error = %v, expected pass = %v", err, tc.wantPass)
			}
		})
	}
}

func TestVerifyRequiresEachPackage(t *testing.T) {
	log := `{"Action":"run","Package":"example/a","Test":"TestA"}` + "\n" +
		`{"Action":"pass","Package":"example/a","Test":"TestA"}` + "\n" +
		`{"Action":"pass","Package":"example/a"}` + "\n"
	if err := verify(strings.NewReader(log), []string{"example/a", "example/b"}, nil, 1, false); err == nil {
		t.Fatal("missing second package accepted")
	}
}
