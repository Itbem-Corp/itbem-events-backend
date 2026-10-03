package automationagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"events-stocks/internal/releasegate"
)

func provisionPublishedQASources(ctx context.Context, taskID, runID string, delivery json.RawMessage, acquire func(context.Context, string, string, string, json.RawMessage) (string, error)) error {
	var input struct {
		Gatekeeper json.RawMessage `json:"gatekeeper"`
		Changes    []struct {
			Reference  string `json:"repository_ref"`
			Branch     string `json:"branch"`
			SHA        string `json:"commit_sha"`
			ReviewType string `json:"review_type"`
			CIStatus   string `json:"ci_status"`
			Metadata   struct {
				Repository   string `json:"remote_repository"`
				TargetBranch string `json:"target_branch"`
			} `json:"metadata"`
		} `json:"change_sets"`
	}
	if json.Unmarshal(delivery, &input) != nil {
		return fmt.Errorf("QA source input invalid")
	}
	if len(input.Gatekeeper) == 0 {
		return nil
	} // Preserve sealed legacy local handoffs.
	candidate, err := releasegate.DecodeInput(input.Gatekeeper)
	if err != nil {
		return err
	}
	if _, err := releasegate.RevisionMatrixDigest(candidate.Revisions); err != nil {
		return err
	}
	references := make([]string, 0, len(candidate.Revisions))
	seen := map[string]bool{}
	for _, revision := range candidate.Revisions {
		matches := 0
		reference := ""
		for _, change := range input.Changes {
			if change.ReviewType == "pull_request" && change.CIStatus == "passed" && strings.EqualFold(change.Metadata.Repository, revision.Repository) && change.Metadata.TargetBranch == revision.Branch && strings.EqualFold(change.SHA, revision.SHA) {
				matches++
				reference = change.Reference
			}
		}
		if matches != 1 || !strings.HasPrefix(reference, "workspace://") || seen[reference] {
			return fmt.Errorf("QA source selection must cover the exact frozen matrix without duplicates")
		}
		seen[reference] = true
		references = append(references, reference)
	}
	if acquire == nil {
		return fmt.Errorf("QA source acquisition requires a signed gateway")
	}
	for _, reference := range references {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := acquire(ctx, taskID, runID, reference, delivery); err != nil {
			return err
		}
	}
	return nil
}
