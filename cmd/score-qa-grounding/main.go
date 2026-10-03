// score-qa-grounding compares model claims with a separately captured QA ledger.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"events-stocks/internal/qaevidence"
)

func readInput(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 65536 {
		return nil, fmt.Errorf("input size must be 1..65536 bytes")
	}
	return data, nil
}

// Publish only completed evidence. A hard link atomically creates the final
// name without replacing an existing file; interrupted writes retain no final
// score. Filesystems lacking hard-link support fail explicitly.
func writeScore(path string, payload []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".qa-score-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	n, err := file.Write(payload)
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Link(file.Name(), path)
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("score-qa-grounding", flag.ContinueOnError)
	flags.SetOutput(stderr)
	observationPath := flags.String("observation", "", "runtime observation JSON")
	claimsPath := flags.String("claims", "", "model claims JSON")
	outputPath := flags.String("output", "", "optional score JSON file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *observationPath == "" || *claimsPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "-observation and -claims are required; positional arguments are not accepted")
		return 2
	}
	result := struct {
		SchemaVersion   int      `json:"schema_version"`
		Passed          bool     `json:"passed"`
		ScoreKind       string   `json:"score_kind"`
		ObservedVerdict string   `json:"observed_qa_verdict,omitempty"`
		ClaimedVerdict  string   `json:"claimed_qa_verdict,omitempty"`
		ObservationHash string   `json:"observation_sha256,omitempty"`
		ClaimsHash      string   `json:"claims_sha256,omitempty"`
		Errors          []string `json:"errors"`
		Limitations     []string `json:"limitations"`
	}{SchemaVersion: 1, ScoreKind: "structured_qa_grounding", Errors: []string{}, Limitations: []string{"Checks supplied structured claims against supplied observations; does not authenticate their source.", "Does not score free-form prose, model quality or authorize release."}}
	hash := func(data []byte) string { digest := sha256.Sum256(data); return hex.EncodeToString(digest[:]) }
	evaluate := func() error {
		raw, err := readInput(*observationPath)
		if err != nil {
			return fmt.Errorf("observation input: %w", err)
		}
		result.ObservationHash = hash(raw)
		claimsRaw, claimsErr := readInput(*claimsPath)
		if claimsErr == nil {
			result.ClaimsHash = hash(claimsRaw)
		}
		observation, err := qaevidence.Decode(raw)
		if err != nil {
			return err
		}
		result.ObservedVerdict = "passed"
		if !observation.PreviewPassed {
			result.ObservedVerdict = "failed"
		}
		for _, repository := range observation.Repositories {
			for _, command := range repository.Commands {
				if !command.Passed {
					result.ObservedVerdict = "failed"
				}
			}
		}
		if claimsErr != nil {
			return fmt.Errorf("claims input: %w", claimsErr)
		}
		claims, err := qaevidence.DecodeClaims(claimsRaw)
		if err != nil {
			return err
		}
		result.ClaimedVerdict = claims.Verdict
		return qaevidence.ValidateGrounding(observation, claims)
	}
	if err := evaluate(); err != nil {
		result.Errors = append(result.Errors, err.Error())
	} else {
		result.Passed = true
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	encoded = append(encoded, '\n')
	if *outputPath != "" {
		if err := writeScore(*outputPath, encoded); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if _, err := stdout.Write(encoded); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !result.Passed {
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
