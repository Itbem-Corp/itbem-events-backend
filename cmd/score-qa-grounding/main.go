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
		ObservationHash string   `json:"observation_sha256,omitempty"`
		ClaimsHash      string   `json:"claims_sha256,omitempty"`
		Errors          []string `json:"errors"`
		Limitations     []string `json:"limitations"`
	}{SchemaVersion: 1, Errors: []string{}, Limitations: []string{"Checks supplied structured claims against supplied observations; does not authenticate their source.", "Does not score free-form prose, model quality or authorize release."}}
	hash := func(data []byte) string { digest := sha256.Sum256(data); return hex.EncodeToString(digest[:]) }
	evaluate := func() error {
		raw, err := readInput(*observationPath)
		if err != nil {
			return fmt.Errorf("observation input: %w", err)
		}
		result.ObservationHash = hash(raw)
		observation, err := qaevidence.Decode(raw)
		if err != nil {
			return err
		}
		raw, err = readInput(*claimsPath)
		if err != nil {
			return fmt.Errorf("claims input: %w", err)
		}
		result.ClaimsHash = hash(raw)
		claims, err := qaevidence.DecodeClaims(raw)
		if err != nil {
			return err
		}
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
		// Evidence is write-once: exclusive creation also protects input paths,
		// hard-link aliases and symlinks from being overwritten by a score.
		file, err := os.OpenFile(*outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		_, writeErr := file.Write(encoded)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			fmt.Fprintln(stderr, "score output could not be completed", writeErr, closeErr)
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
