// Package environmentevidence defines the bounded, value-free observation
// emitted by the deterministic release worker for one exact revision matrix.
package environmentevidence

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
	maxInputBytes = 128 << 10
	maxReferences = 64
)

type Repository struct {
	Repository                 string   `json:"repository"`
	HeadSHA                    string   `json:"head_sha"`
	Workflow                   string   `json:"workflow"`
	Environment                string   `json:"environment"`
	RequiredSecretReferences   []string `json:"required_secret_references"`
	RequiredVariableReferences []string `json:"required_variable_references"`
	WorkflowExists             bool     `json:"workflow_exists"`
	EnvironmentExists          bool     `json:"environment_exists"`
	MissingSecretReferences    []string `json:"missing_secret_references"`
	MissingVariableReferences  []string `json:"missing_variable_references"`
}

func (repository Repository) Ready() bool {
	return repository.WorkflowExists && repository.EnvironmentExists && len(repository.MissingSecretReferences) == 0 && len(repository.MissingVariableReferences) == 0
}

type Observation struct {
	SchemaVersion int          `json:"schema_version"`
	TaskID        string       `json:"task_id"`
	MatrixDigest  string       `json:"matrix_digest"`
	Repositories  []Repository `json:"repositories"`
}

var (
	uuidPattern       = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[1-5][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	repositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)
	shaPattern        = regexp.MustCompile(`^[a-f0-9]{40}$`)
	workflowPattern   = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]{0,119}\.ya?ml$`)
	referencePattern  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

func Decode(payload []byte) (Observation, error) {
	if len(payload) == 0 || len(payload) > maxInputBytes {
		return Observation{}, fmt.Errorf("environment observation size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var observation Observation
	if err := decoder.Decode(&observation); err != nil {
		return Observation{}, fmt.Errorf("decode environment observation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Observation{}, fmt.Errorf("environment observation must contain one JSON document")
	}
	if err := Validate(observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func Validate(observation Observation) error {
	if observation.SchemaVersion != SchemaVersion || !uuidPattern.MatchString(strings.ToLower(strings.TrimSpace(observation.TaskID))) || !digestPattern.MatchString(strings.ToLower(strings.TrimSpace(observation.MatrixDigest))) || len(observation.Repositories) == 0 || len(observation.Repositories) > 64 {
		return fmt.Errorf("environment observation identity is invalid")
	}
	seen := make(map[string]struct{}, len(observation.Repositories))
	for _, repository := range observation.Repositories {
		if err := validateRepository(repository); err != nil {
			return err
		}
		if _, duplicate := seen[repository.Repository]; duplicate {
			return fmt.Errorf("environment repository observation is duplicated")
		}
		seen[repository.Repository] = struct{}{}
	}
	return nil
}

func validateRepository(repository Repository) error {
	if repository.Repository != strings.ToLower(strings.TrimSpace(repository.Repository)) || !repositoryPattern.MatchString(repository.Repository) || repository.HeadSHA != strings.ToLower(strings.TrimSpace(repository.HeadSHA)) || !shaPattern.MatchString(repository.HeadSHA) || repository.Workflow != strings.TrimSpace(repository.Workflow) || !workflowPattern.MatchString(repository.Workflow) || repository.Environment != strings.TrimSpace(repository.Environment) || repository.Environment == "" || len(repository.Environment) > 128 || strings.IndexFunc(repository.Environment, func(character rune) bool { return character < 0x20 || character == 0x7f }) != -1 {
		return fmt.Errorf("environment repository observation is invalid")
	}
	for _, values := range [][]string{repository.RequiredSecretReferences, repository.RequiredVariableReferences, repository.MissingSecretReferences, repository.MissingVariableReferences} {
		if !validReferences(values) {
			return fmt.Errorf("environment repository references are invalid")
		}
	}
	if !subset(repository.MissingSecretReferences, repository.RequiredSecretReferences) || !subset(repository.MissingVariableReferences, repository.RequiredVariableReferences) {
		return fmt.Errorf("environment missing references are not required by policy")
	}
	if !repository.WorkflowExists || !repository.EnvironmentExists {
		// A failed remote read is an operation failure and must not be serialized
		// as a partial success. Successfully observed absence is represented by a
		// complete observation with readiness false only for missing references.
		return fmt.Errorf("environment remote resources were not observed")
	}
	return nil
}

func Canonical(observation Observation) (Observation, error) {
	for _, repository := range observation.Repositories {
		if repository.RequiredSecretReferences == nil || repository.RequiredVariableReferences == nil || repository.MissingSecretReferences == nil || repository.MissingVariableReferences == nil {
			return Observation{}, fmt.Errorf("environment repository references must be explicit")
		}
	}
	clone := observation
	clone.TaskID = strings.ToLower(strings.TrimSpace(observation.TaskID))
	clone.MatrixDigest = strings.ToLower(strings.TrimSpace(observation.MatrixDigest))
	clone.Repositories = append([]Repository(nil), observation.Repositories...)
	for index := range clone.Repositories {
		repository := &clone.Repositories[index]
		repository.RequiredSecretReferences = canonicalReferences(repository.RequiredSecretReferences)
		repository.RequiredVariableReferences = canonicalReferences(repository.RequiredVariableReferences)
		repository.MissingSecretReferences = canonicalReferences(repository.MissingSecretReferences)
		repository.MissingVariableReferences = canonicalReferences(repository.MissingVariableReferences)
	}
	if err := Validate(clone); err != nil {
		return Observation{}, err
	}
	sort.Slice(clone.Repositories, func(left, right int) bool {
		return clone.Repositories[left].Repository < clone.Repositories[right].Repository
	})
	return clone, nil
}

func canonicalReferences(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToUpper(strings.TrimSpace(value))
	}
	sort.Strings(result)
	return result
}

func validReferences(values []string) bool {
	if values == nil || len(values) > maxReferences {
		return false
	}
	for index, value := range values {
		if value != strings.ToUpper(strings.TrimSpace(value)) || !referencePattern.MatchString(value) || strings.HasPrefix(value, "GITHUB_") || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func subset(values, allowed []string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		allowedSet[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := allowedSet[value]; !ok {
			return false
		}
	}
	return true
}
