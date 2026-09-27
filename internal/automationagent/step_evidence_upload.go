package automationagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"events-stocks/services/deliveryplansteps"
	"github.com/gofrs/uuid"
)

type generatedPlanStepEvidenceReport struct {
	SchemaVersion int                      `json:"schema_version"`
	PlanID        string                   `json:"plan_id"`
	PlanVersion   int                      `json:"plan_version"`
	StepID        string                   `json:"step_id"`
	StepKey       string                   `json:"step_key"`
	ReviewDiffSHA string                   `json:"review_diff_sha256"`
	Checks        []generatedPlanStepCheck `json:"checks"`
}

type generatedPlanStepCheck struct {
	CriterionSHA256 string `json:"criterion_sha256"`
	Passed          bool   `json:"passed"`
}

// uploadRequiredPlanStepEvidence creates and uploads only the runtime-owned
// acceptance report format. Requirements with unknown artifact semantics are
// never satisfied by a guessed/model-authored file; a required unknown type
// blocks completion, while optional unsupported evidence remains absent.
func uploadRequiredPlanStepEvidence(step *planStepExecution, evidence planStepEvidence) error {
	if step == nil || step.claim.Step == nil {
		return fmt.Errorf("plan-step evidence identity is unavailable")
	}
	for _, requirement := range step.claim.Step.EvidenceRequirements {
		if !isGeneratedPlanStepReportKey(requirement.Key) {
			if requirement.Required {
				return fmt.Errorf("required plan-step evidence cannot be generated for its registered requirement")
			}
			continue
		}
		contentType := supportedPlanStepReportContentType(requirement.ContentTypes)
		if contentType == "" || requirement.MaxBytes < 1 || requirement.MaxBytes > deliveryplansteps.MaxStepEvidenceBytes {
			if requirement.Required {
				return fmt.Errorf("required plan-step evidence format is unsupported")
			}
			continue
		}
		body, err := encodePlanStepEvidenceReport(step.claim.Step, evidence)
		if err != nil {
			if requirement.Required {
				return fmt.Errorf("required plan-step evidence could not be generated")
			}
			continue
		}
		if int64(len(body)) > requirement.MaxBytes || len(body) > maxPlanStepEvidenceUploadBytes || scanPlanStepEvidenceBytes(body) != nil {
			wipeBytes(body)
			if requirement.Required {
				return fmt.Errorf("required plan-step evidence exceeds its safe format or size")
			}
			continue
		}
		digest := sha256.Sum256(body)
		eventID := planStepEvidenceEventID(step.lease, requirement.Key, hex.EncodeToString(digest[:]))
		_, uploadErr := step.UploadEvidence(step.ctx, requirement.Key, requirement.Key+".json", contentType, eventID, body)
		wipeBytes(body)
		if uploadErr != nil && requirement.Required {
			return fmt.Errorf("required plan-step evidence upload failed")
		}
	}
	return nil
}

func planStepEvidenceEventID(lease PlanStepLeaseRequest, requirementKey, bodySHA256 string) string {
	// Idempotency is scoped to one lease owner. In particular, a takeover gets a
	// new event ID when its fencing token changes, so the server can append
	// evidence bound to the new fence instead of colliding with the stale
	// owner's event. The remaining identity values are already part of the
	// signed lease callback tuple and prevent reuse across tasks, runs, workers,
	// agents, or machines.
	identity := strings.Join([]string{
		"itbem/step-evidence",
		lease.TaskID,
		lease.RunID,
		lease.StepID,
		lease.WorkerID,
		lease.AgentKey,
		lease.MachineID,
		lease.FencingToken,
		requirementKey,
		bodySHA256,
	}, "/")
	return uuid.NewV5(uuid.NamespaceURL, identity).String()
}

func isGeneratedPlanStepReportKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "acceptance-report", "acceptance_report":
		return true
	default:
		return false
	}
}

func supportedPlanStepReportContentType(allowed []string) string {
	for _, contentType := range allowed {
		if contentType == "application/json" {
			return contentType
		}
	}
	for _, contentType := range allowed {
		if contentType == "text/plain" {
			return contentType
		}
	}
	return ""
}

func encodePlanStepEvidenceReport(step *PlanStepDTO, evidence planStepEvidence) ([]byte, error) {
	if step == nil || evidence.StepID != step.ID || evidence.PlanID != step.PlanID || evidence.PlanVersion != step.PlanVersion || !validAgentSHA256(evidence.ReviewDiffSHA256) || len(evidence.Checks) == 0 {
		return nil, fmt.Errorf("verified evidence does not match its claimed plan step")
	}
	report := generatedPlanStepEvidenceReport{
		SchemaVersion: 1, PlanID: step.PlanID, PlanVersion: step.PlanVersion, StepID: step.ID,
		StepKey: step.StepKey, ReviewDiffSHA: evidence.ReviewDiffSHA256,
		Checks: make([]generatedPlanStepCheck, 0, len(evidence.Checks)),
	}
	for _, criterion := range evidence.AcceptanceCriteria {
		digest := sha256.Sum256([]byte(strings.TrimSpace(criterion)))
		report.Checks = append(report.Checks, generatedPlanStepCheck{CriterionSHA256: hex.EncodeToString(digest[:]), Passed: true})
	}
	if len(report.Checks) == 0 {
		return nil, fmt.Errorf("verified acceptance checks are unavailable")
	}
	return json.Marshal(report)
}
