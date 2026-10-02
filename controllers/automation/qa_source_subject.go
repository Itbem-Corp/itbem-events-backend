package automation

import (
	"encoding/json"
	"fmt"
	"strings"

	"events-stocks/internal/releasegate"
	"events-stocks/models"
)

type qaSourceSubject struct {
	Reference  string
	Repository string
	Branch     string
	SHA        string
}

// Only the immutable server input supplies source coordinates. The caller
// must separately validate the live task and signed enrolled instance.
func qaSourceSubjectForTask(task *models.AutomationTask, delivery json.RawMessage, reference string) (qaSourceSubject, error) {
	deny := func() (qaSourceSubject, error) {
		return qaSourceSubject{}, fmt.Errorf("QA source does not match the frozen task subject")
	}
	if task == nil || task.Operation != "delivery.qa" || task.DeliveryWorkItemID == nil || !artifactDigestPattern.MatchString(task.EvidenceSubjectDigest) || !strings.HasPrefix(reference, "workspace://") || len(delivery) == 0 || len(delivery) > gatewayMaxObjectBytes {
		return deny()
	}
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
		return deny()
	}
	candidate, err := releasegate.DecodeInput(input.Gatekeeper)
	if err != nil || candidate.ChangeSetID != task.DeliveryWorkItemID.String() {
		return deny()
	}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil || digest != task.EvidenceSubjectDigest {
		return deny()
	}
	var selected qaSourceSubject
	matches := 0
	for _, change := range input.Changes {
		if change.Reference != reference || change.ReviewType != "pull_request" || change.CIStatus != "passed" {
			continue
		}
		for _, revision := range candidate.Revisions {
			if strings.EqualFold(change.Metadata.Repository, revision.Repository) && change.Metadata.TargetBranch == revision.Branch && strings.EqualFold(change.SHA, revision.SHA) {
				matches++
				selected = qaSourceSubject{Reference: reference, Repository: revision.Repository, Branch: change.Branch, SHA: revision.SHA}
			}
		}
	}
	if matches != 1 || !agentBranchPattern.MatchString(selected.Branch) {
		return deny()
	}
	return selected, nil
}
