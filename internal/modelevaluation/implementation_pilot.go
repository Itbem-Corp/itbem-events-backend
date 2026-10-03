package modelevaluation

import (
	_ "embed"
	"encoding/json"
	"errors"
)

// ImplementationPilotVersion is prepared offline; SupportedCorpus deliberately
// excludes it until central admission and dispatch support the pilot cardinality.
const ImplementationPilotVersion = "synthetic-implementation-pagination-2026-10-03-v1"

//go:embed implementation_pilot_corpus.json
var implementationPilotJSON []byte

// PrepareImplementationPilot reserves the three existing routes for a single
// frozen case. It admits no request prompts and makes no provider calls.
func PrepareImplementationPilot(messageOverheadBytes int, pricing string) (Plan, error) {
	var value struct {
		Version string `json:"corpus_version"`
		System  string `json:"system"`
		Cases   []Case `json:"cases"`
	}
	if err := json.Unmarshal(implementationPilotJSON, &value); err != nil {
		return Plan{}, err
	}
	if value.Version != ImplementationPilotVersion || len(value.Cases) != 1 || value.Cases[0].ID != "pagination-v1" {
		return Plan{}, errors.New("invalid frozen implementation pilot")
	}
	return compile(value.Cases, value.System, messageOverheadBytes, pricing, 1)
}
