// verify-test-evidence checks execution evidence in go test -json logs.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

type names []string

func (n *names) String() string         { return fmt.Sprint([]string(*n)) }
func (n *names) Set(value string) error { *n = append(*n, value); return nil }

type event struct{ Action, Package, Test string }
type executions struct{ runs, passes, skips int }

func verify(reader io.Reader, packages, requiredTests []string, repetitions int, allowSkips bool) error {
	if repetitions < 1 || len(packages) == 0 {
		return fmt.Errorf("packages and positive repetitions are required")
	}
	wanted := map[string]bool{}
	for _, pkg := range packages {
		if pkg == "" || wanted[pkg] {
			return fmt.Errorf("invalid or duplicate package")
		}
		wanted[pkg] = true
	}
	completed := map[string]int{}
	tests := map[string]map[string]*executions{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var row event
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return fmt.Errorf("invalid JSONL evidence: %w", err)
		}
		if !wanted[row.Package] {
			return fmt.Errorf("unexpected or missing package: %q", row.Package)
		}
		if completed[row.Package] != 0 {
			return fmt.Errorf("event after package completion: %s", row.Package)
		}
		switch row.Action {
		case "start", "run", "pause", "cont", "output", "pass", "skip":
		case "fail":
			return fmt.Errorf("failed execution: %s %s", row.Package, row.Test)
		default:
			return fmt.Errorf("unknown event action: %q", row.Action)
		}
		if row.Test == "" {
			if row.Action == "pass" {
				completed[row.Package]++
			}
			if row.Action == "skip" {
				return fmt.Errorf("package was skipped: %s", row.Package)
			}
			continue
		}
		if tests[row.Package] == nil {
			tests[row.Package] = map[string]*executions{}
		}
		if tests[row.Package][row.Test] == nil {
			tests[row.Package][row.Test] = &executions{}
		}
		state := tests[row.Package][row.Test]
		switch row.Action {
		case "run":
			state.runs++
		case "pass":
			state.passes++
		case "skip":
			state.skips++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read evidence: %w", err)
	}
	for _, pkg := range packages {
		if completed[pkg] != 1 || len(tests[pkg]) == 0 {
			return fmt.Errorf("missing unique completion or tests: %s", pkg)
		}
		passed := false
		for name, state := range tests[pkg] {
			if state.runs != repetitions || state.passes+state.skips != repetitions || (!allowSkips && state.skips != 0) {
				return fmt.Errorf("incomplete or skipped repetitions: %s %s", pkg, name)
			}
			passed = passed || state.passes > 0
		}
		if !passed {
			return fmt.Errorf("no passing tests: %s", pkg)
		}
	}
	for _, name := range requiredTests {
		found := 0
		for _, pkg := range packages {
			if state := tests[pkg][name]; state != nil && state.passes == repetitions && state.skips == 0 {
				found++
			}
		}
		if found != 1 {
			return fmt.Errorf("required test lacks unambiguous passing evidence: %s", name)
		}
	}
	return nil
}

func main() {
	var packages, tests names
	flag.Var(&packages, "package", "expected Go import path; repeat for each package")
	flag.Var(&tests, "test", "required passing test name; repeat for each test")
	path := flag.String("log", "", "go test JSONL file")
	repetitions := flag.Int("repetitions", 1, "required executions per test")
	allowSkips := flag.Bool("allow-skips", false, "allow explicitly recorded optional test skips")
	flag.Parse()
	file, err := os.Open(*path)
	if err == nil {
		defer file.Close()
		err = verify(file, packages, tests, *repetitions, *allowSkips)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Go execution evidence verified.")
}
