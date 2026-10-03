package qaevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Claims is a model's machine-readable account, kept separate from Observation.
// Pointer results distinguish an omitted result from an explicit false value.
type Claims struct {
	SchemaVersion int            `json:"schema_version"`
	TaskID        string         `json:"task_id"`
	MatrixDigest  string         `json:"matrix_digest"`
	PreviewPassed *bool          `json:"preview_passed"`
	Verdict       string         `json:"verdict"`
	Commands      []CommandClaim `json:"commands"`
}

type CommandClaim struct {
	Reference string `json:"reference"`
	Index     *int   `json:"index"`
	Phase     string `json:"phase"`
	Kind      string `json:"kind"`
	Passed    *bool  `json:"passed"`
}

func DecodeClaims(payload []byte) (Claims, error) {
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return Claims{}, fmt.Errorf("QA claims size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims Claims
	if err := decoder.Decode(&claims); err != nil {
		return Claims{}, fmt.Errorf("decode QA claims: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Claims{}, fmt.Errorf("QA claims must contain one JSON document")
	}
	return claims, nil
}

// ValidateGrounding checks factual correspondence and complete command coverage.
// The caller must obtain the observation from the trusted runtime ledger. This
// comparison authenticates neither supplied files nor free-form narrative.
func ValidateGrounding(observation Observation, claims Claims) error {
	if err := Validate(observation); err != nil {
		return err
	}
	if claims.SchemaVersion != 1 || claims.TaskID != observation.TaskID || claims.MatrixDigest != observation.MatrixDigest {
		return fmt.Errorf("QA claims do not identify the observed task and revision matrix")
	}
	if claims.PreviewPassed == nil || *claims.PreviewPassed != observation.PreviewPassed {
		return fmt.Errorf("QA preview claim differs from observation")
	}
	type key struct {
		reference string
		index     int
	}
	expected := map[key]Command{}
	failed := !observation.PreviewPassed
	for _, repository := range observation.Repositories {
		for _, command := range repository.Commands {
			expected[key{repository.Reference, command.Index}] = command
			failed = failed || !command.Passed
		}
	}
	verdict := "passed"
	if failed {
		verdict = "failed"
	}
	if claims.Verdict != verdict {
		return fmt.Errorf("QA verdict differs from observed results")
	}
	if claims.Commands == nil || len(claims.Commands) != len(expected) {
		return fmt.Errorf("QA claims require complete observed command coverage")
	}
	seen := map[key]bool{}
	for _, claim := range claims.Commands {
		if claim.Index == nil || claim.Passed == nil {
			return fmt.Errorf("QA command claim requires explicit index and result")
		}
		identity := key{claim.Reference, *claim.Index}
		command, exists := expected[identity]
		if !exists || seen[identity] {
			return fmt.Errorf("QA command claim is unknown or duplicated")
		}
		seen[identity] = true
		if claim.Phase != command.Phase || claim.Kind != command.Kind || *claim.Passed != command.Passed {
			return fmt.Errorf("QA command claim differs from observed phase, kind or result")
		}
	}
	return nil
}
