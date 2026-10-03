package modelevaluation

import (
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
)

// ImplementationPilotVersion is prepared offline; SupportedCorpus deliberately
// excludes it until central admission and dispatch support the pilot cardinality.
const ImplementationPilotVersion = "synthetic-implementation-pagination-2026-10-03-v1"

//go:embed implementation_pilot_corpus.json
var implementationPilotJSON []byte

type implementationPilotCorpus struct {
	Version string `json:"corpus_version"`
	System  string `json:"system"`
	Cases   []Case `json:"cases"`
}

func readImplementationPilot() (implementationPilotCorpus, error) {
	var value implementationPilotCorpus
	if err := json.Unmarshal(implementationPilotJSON, &value); err != nil {
		return value, err
	}
	if value.Version != ImplementationPilotVersion || len(value.Cases) != 1 || value.Cases[0].ID != "pagination-v1" {
		return value, errors.New("invalid frozen implementation pilot")
	}
	return value, nil
}

// ImplementationPilotInput returns the server-owned prompt and corpus digest.
// It performs no inference and contains neither reference source nor oracle.
func ImplementationPilotInput() (string, string, error) {
	value, err := readImplementationPilot()
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(value.System) + "\n\n" + strings.TrimSpace(value.Cases[0].Prompt), Digest(implementationPilotJSON), nil
}

// PrepareImplementationPilot reserves the three existing routes for a single
// frozen case. It admits no request prompts and makes no provider calls.
func PrepareImplementationPilot(messageOverheadBytes int, pricing string) (Plan, error) {
	value, err := readImplementationPilot()
	if err != nil {
		return Plan{}, err
	}
	return compile(value.Cases, value.System, messageOverheadBytes, pricing, 1)
}
