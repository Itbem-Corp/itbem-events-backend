package modelevaluation

import (
	_ "embed"
	"encoding/json"
)

const CorpusVersion = "synthetic-screening-20-2026-09-30-v1"

//go:embed corpus.json
var corpusJSON []byte

// Corpus returns a newly decoded copy of the versioned server-owned evidence.
// Expected answers stay outside inference input and are evaluated offline.
func Corpus() ([]Case, string, string, error) {
	var value struct {
		System string `json:"system"`
		Cases  []Case `json:"cases"`
	}
	if err := json.Unmarshal(corpusJSON, &value); err != nil {
		return nil, "", "", err
	}
	return value.Cases, value.System, Digest(corpusJSON), nil
}
