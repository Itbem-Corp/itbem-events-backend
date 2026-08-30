// Package qaevidence defines the bounded, provider-independent observation
// emitted by the isolated QA runner. It records what ran; it never decides a
// merge or release gate.
package qaevidence

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
)

type Command struct {
	Index  int    `json:"index"`
	Phase  string `json:"phase"`
	Passed bool   `json:"passed"`
}

type Repository struct {
	Reference string    `json:"reference"`
	Branch    string    `json:"branch"`
	Commands  []Command `json:"commands"`
}

type Observation struct {
	SchemaVersion            int          `json:"schema_version"`
	TaskID                   string       `json:"task_id"`
	MatrixDigest             string       `json:"matrix_digest"`
	PreviewPassed            bool         `json:"preview_passed"`
	RepositoryExecutionOrder []string     `json:"repository_execution_order"`
	Repositories             []Repository `json:"repositories"`
}

var (
	uuidPattern      = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	digestPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	workspacePattern = regexp.MustCompile(`^workspace://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	branchPattern    = regexp.MustCompile(`^itbem-agent/[a-f0-9-]{36}$`)
)

func Decode(payload []byte) (Observation, error) {
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return Observation{}, fmt.Errorf("QA observation size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var observation Observation
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, fmt.Errorf("decode QA observation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Observation{}, fmt.Errorf("QA observation must contain one JSON document")
	}
	if err := Validate(observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func Validate(observation Observation) error {
	if observation.SchemaVersion != SchemaVersion || !uuidPattern.MatchString(strings.ToLower(strings.TrimSpace(observation.TaskID))) ||
		!digestPattern.MatchString(strings.ToLower(strings.TrimSpace(observation.MatrixDigest))) || len(observation.Repositories) == 0 || len(observation.Repositories) > 64 ||
		len(observation.RepositoryExecutionOrder) != len(observation.Repositories) {
		return fmt.Errorf("QA observation identity is invalid")
	}
	seenRepositories := make(map[string]struct{}, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		reference, branch := strings.TrimSpace(repository.Reference), strings.TrimSpace(repository.Branch)
		if reference != repository.Reference || branch != repository.Branch || !workspacePattern.MatchString(reference) || !branchPattern.MatchString(strings.ToLower(branch)) || len(repository.Commands) > 12 {
			return fmt.Errorf("QA repository observation is invalid")
		}
		if _, duplicate := seenRepositories[reference]; duplicate {
			return fmt.Errorf("QA repository observation is duplicated")
		}
		seenRepositories[reference] = struct{}{}
		seenCommands := make(map[int]struct{}, len(repository.Commands))
		for _, command := range repository.Commands {
			if command.Index < 0 || command.Index >= 12 || (command.Phase != "validation" && command.Phase != "qa") {
				return fmt.Errorf("QA command observation is invalid")
			}
			if _, duplicate := seenCommands[command.Index]; duplicate {
				return fmt.Errorf("QA command observation is duplicated")
			}
			seenCommands[command.Index] = struct{}{}
		}
	}
	seenOrder := make(map[string]struct{}, len(observation.RepositoryExecutionOrder))
	for _, reference := range observation.RepositoryExecutionOrder {
		if _, exists := seenRepositories[reference]; !exists {
			return fmt.Errorf("QA repository execution order is invalid")
		}
		if _, duplicate := seenOrder[reference]; duplicate {
			return fmt.Errorf("QA repository execution order is duplicated")
		}
		seenOrder[reference] = struct{}{}
	}
	return nil
}

// Canonical returns a stable copy for hashing and append-only persistence.
// Execution order remains explicit while repository object order is ignored.
func Canonical(observation Observation) (Observation, error) {
	if err := Validate(observation); err != nil {
		return Observation{}, err
	}
	clone := observation
	clone.TaskID = strings.ToLower(strings.TrimSpace(clone.TaskID))
	clone.MatrixDigest = strings.ToLower(strings.TrimSpace(clone.MatrixDigest))
	clone.RepositoryExecutionOrder = append([]string(nil), observation.RepositoryExecutionOrder...)
	clone.Repositories = append([]Repository(nil), observation.Repositories...)
	for index := range clone.Repositories {
		clone.Repositories[index].Commands = append([]Command(nil), clone.Repositories[index].Commands...)
		sort.Slice(clone.Repositories[index].Commands, func(left, right int) bool {
			return clone.Repositories[index].Commands[left].Index < clone.Repositories[index].Commands[right].Index
		})
	}
	sort.Slice(clone.Repositories, func(left, right int) bool {
		return clone.Repositories[left].Reference < clone.Repositories[right].Reference
	})
	return clone, nil
}
