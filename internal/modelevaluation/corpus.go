package modelevaluation

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

const CorpusVersion = "synthetic-screening-20-2026-09-30-v1"
const CacheCorpusVersion = "synthetic-prefix-cache-20-2026-10-01-v1"

//go:embed corpus.json
var corpusJSON []byte

//go:embed cache_corpus.json
var cacheCorpusJSON []byte

func SupportedCorpus(version string) bool {
	return version == CorpusVersion || version == CacheCorpusVersion || version == ImplementationPilotVersion
}

// Corpus returns a newly decoded copy of the versioned server-owned evidence.
// Expected answers stay outside inference input and are evaluated offline.
func Corpus() ([]Case, string, string, error) {
	return CorpusForVersion(CorpusVersion)
}

func CorpusForVersion(version string) ([]Case, string, string, error) {
	var source []byte
	switch version {
	case CorpusVersion:
		source = corpusJSON
	case CacheCorpusVersion:
		source = cacheCorpusJSON
	case ImplementationPilotVersion:
		value, err := readImplementationPilot()
		return value.Cases, value.System, Digest(implementationPilotJSON), err
	default:
		return nil, "", "", fmt.Errorf("unsupported evaluation corpus %q", version)
	}
	var value struct {
		System string `json:"system"`
		Cases  []Case `json:"cases"`
	}
	if err := json.Unmarshal(source, &value); err != nil {
		return nil, "", "", err
	}
	return value.Cases, value.System, Digest(source), nil
}
