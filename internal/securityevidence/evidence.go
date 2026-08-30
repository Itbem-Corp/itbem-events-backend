// Package securityevidence defines the bounded local security observation
// emitted by an isolated QA worker. It records scan state for an exact
// workspace matrix; only the Gatekeeper decides release authority.
package securityevidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const (
	SchemaVersion = 1
	maxInputBytes = 64 << 10
	MaxFindings   = 100
)

type Repository struct {
	Reference        string `json:"reference"`
	Branch           string `json:"branch"`
	SecretScanPassed bool   `json:"secret_scan_passed"`
	HighFindings     int    `json:"high_findings"`
	CriticalFindings int    `json:"critical_findings"`
}

type Observation struct {
	SchemaVersion int          `json:"schema_version"`
	TaskID        string       `json:"task_id"`
	MatrixDigest  string       `json:"matrix_digest"`
	Repositories  []Repository `json:"repositories"`
}

var (
	uuidPattern      = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	digestPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	workspacePattern = regexp.MustCompile(`^workspace://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	branchPattern    = regexp.MustCompile(`^itbem-agent/[a-f0-9-]{36}$`)
)

func Decode(payload []byte) (Observation, error) {
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return Observation{}, fmt.Errorf("security observation size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var observation Observation
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, fmt.Errorf("decode security observation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Observation{}, fmt.Errorf("security observation must contain one JSON document")
	}
	if err := Validate(observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func Validate(observation Observation) error {
	if observation.SchemaVersion != SchemaVersion || !uuidPattern.MatchString(strings.ToLower(strings.TrimSpace(observation.TaskID))) ||
		!digestPattern.MatchString(strings.ToLower(strings.TrimSpace(observation.MatrixDigest))) || len(observation.Repositories) == 0 || len(observation.Repositories) > 64 {
		return fmt.Errorf("security observation identity is invalid")
	}
	seen := make(map[string]struct{}, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		identity := strings.TrimSpace(repository.Reference)
		branch := strings.TrimSpace(repository.Branch)
		if identity != repository.Reference || branch != repository.Branch || branch != strings.ToLower(branch) || !workspacePattern.MatchString(identity) || !branchPattern.MatchString(branch) ||
			repository.HighFindings < 0 || repository.CriticalFindings < 0 || repository.HighFindings > MaxFindings || repository.CriticalFindings > MaxFindings || repository.HighFindings+repository.CriticalFindings > MaxFindings {
			return fmt.Errorf("security repository observation is invalid")
		}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("security repository observation is duplicated")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func Canonical(observation Observation) (Observation, error) {
	if err := Validate(observation); err != nil {
		return Observation{}, err
	}
	clone := observation
	clone.TaskID = strings.ToLower(strings.TrimSpace(clone.TaskID))
	clone.MatrixDigest = strings.ToLower(strings.TrimSpace(clone.MatrixDigest))
	clone.Repositories = append([]Repository(nil), observation.Repositories...)
	sort.Slice(clone.Repositories, func(left, right int) bool {
		return clone.Repositories[left].Reference < clone.Repositories[right].Reference
	})
	return clone, nil
}
