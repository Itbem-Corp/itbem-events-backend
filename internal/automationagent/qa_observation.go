package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"events-stocks/internal/qaevidence"
	"events-stocks/internal/releasegate"
)

// qaReportWithGrounding evaluates the original model response, never a claims
// projection manufactured from the observation. Historical runs without a
// ledger remain explicitly unavailable for grounding.
func qaReportWithGrounding(content string, execution map[string]any) (map[string]any, map[string]any) {
	grounding := qaClaimsGrounding(content, execution)
	grounding["report_valid"] = false
	if grounding["status"] == "failed" {
		return nil, grounding
	}
	report, err := ParseDeliveryQAReport(content)
	if err == nil {
		err = ValidateDeliveryQAReport(report, execution)
	}
	if err != nil {
		grounding["report_error"] = err.Error()
		return nil, grounding
	}
	grounding["report_valid"] = true
	return report, grounding
}

func qaClaimsGrounding(content string, execution map[string]any) map[string]any {
	grounding := map[string]any{"score_kind": "structured_qa_grounding", "status": "unavailable"}
	observed, exists := execution["ledger_observation"]
	if !exists {
		return grounding
	}
	grounding["status"] = "failed"
	reject := func(err error) map[string]any {
		grounding["error"] = err.Error()
		return grounding
	}
	if _, ok := observed.(map[string]any); !ok {
		return reject(fmt.Errorf("QA ledger observation must be an object"))
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		return reject(err)
	}
	observation, err := qaevidence.Decode(raw)
	if err != nil {
		return reject(err)
	}
	claims, err := qaevidence.DecodeReportClaims([]byte(content))
	if err != nil {
		return reject(err)
	}
	if err := qaevidence.ValidateGrounding(observation, claims); err != nil {
		return reject(err)
	}
	grounding["status"] = "passed"
	return grounding
}

func qaPublishedMatrixDigest(ctx context.Context, delivery json.RawMessage, targets []qaTarget) (string, error) {
	var input struct {
		Gatekeeper *struct {
			Revisions []releasegate.Revision `json:"revisions"`
		} `json:"gatekeeper"`
		Changes []struct {
			Reference string `json:"repository_ref"`
			Branch    string `json:"branch"`
			SHA       string `json:"commit_sha"`
			Metadata  struct {
				Repository   string `json:"remote_repository"`
				TargetBranch string `json:"target_branch"`
			} `json:"metadata"`
		} `json:"change_sets"`
	}
	if err := json.Unmarshal(delivery, &input); err != nil {
		return "", err
	}
	if input.Gatekeeper == nil {
		return "", nil
	} // Historical diagnostic contexts have no ledger subject.
	digest, err := releasegate.RevisionMatrixDigest(input.Gatekeeper.Revisions)
	if err != nil {
		return "", err
	}
	if len(targets) != len(input.Gatekeeper.Revisions) {
		return "", fmt.Errorf("QA targets do not cover the frozen published matrix")
	}
	seen := map[int]bool{}
	for _, target := range targets {
		if target.reviewCommitSHA == "" {
			return "", fmt.Errorf("matrix QA requires published commit targets")
		}
		matches := 0
		for _, change := range input.Changes {
			if change.Reference != target.reference || change.Branch != target.branch || strings.ToLower(change.SHA) != target.reviewCommitSHA {
				continue
			}
			remote, err := gitHubOrigin(ctx, target.root)
			if err != nil || !publicationRepositoryMatches(remote, change.Metadata.Repository) {
				return "", fmt.Errorf("QA repository origin does not match the published subject")
			}
			for index, revision := range input.Gatekeeper.Revisions {
				if strings.EqualFold(revision.Repository, change.Metadata.Repository) && revision.Branch == change.Metadata.TargetBranch && strings.EqualFold(revision.SHA, target.reviewCommitSHA) {
					if seen[index] {
						return "", fmt.Errorf("QA published matrix identity is duplicated")
					}
					seen[index], matches = true, matches+1
				}
			}
		}
		if matches != 1 {
			return "", fmt.Errorf("QA target does not match exactly one frozen published revision")
		}
	}
	return digest, nil
}

func qaLedgerObservation(taskID, matrixDigest string, result map[string]any) (map[string]any, error) {
	if matrixDigest == "" {
		return nil, nil
	}
	// Decode only deterministic execution fields; model summaries never enter the ledger.
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var execution struct {
		Preview struct {
			Passed bool `json:"passed"`
		} `json:"preview"`
		Order []string `json:"repository_execution_order"`
		Runs  []struct {
			Reference string `json:"workspace"`
			Branch    string `json:"branch"`
			Commands  []struct {
				Phase  string `json:"phase"`
				Kind   string `json:"kind"`
				Passed bool   `json:"passed"`
			} `json:"commands"`
		} `json:"repository_runs"`
	}
	if err := json.Unmarshal(raw, &execution); err != nil {
		return nil, err
	}
	observation := qaevidence.Observation{SchemaVersion: qaevidence.SchemaVersion, TaskID: taskID, MatrixDigest: matrixDigest, PreviewPassed: execution.Preview.Passed, RepositoryExecutionOrder: execution.Order}
	for _, run := range execution.Runs {
		repository := qaevidence.Repository{Reference: run.Reference, Branch: run.Branch, Commands: []qaevidence.Command{}}
		for index, command := range run.Commands {
			repository.Commands = append(repository.Commands, qaevidence.Command{Index: index, Phase: command.Phase, Kind: command.Kind, Passed: command.Passed})
		}
		observation.Repositories = append(observation.Repositories, repository)
	}
	if err := qaevidence.Validate(observation); err != nil {
		return nil, err
	}
	raw, err = json.Marshal(observation)
	if err != nil {
		return nil, err
	}
	// Apply the same bounded wire decoder used by the callback before storing
	// a result that recovery would otherwise repeatedly try to deliver.
	if _, err := qaevidence.Decode(raw); err != nil {
		return nil, err
	}
	var handoff map[string]any
	if err := json.Unmarshal(raw, &handoff); err != nil {
		return nil, err
	}
	return handoff, nil
}
