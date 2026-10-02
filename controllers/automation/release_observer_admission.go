package automation

import (
	"fmt"
	"time"

	"events-stocks/internal/agentwork"
	"events-stocks/models"
	"github.com/gofrs/uuid"
)

// This boundary must be checked against the live database task both before
// and after external reads. A queue lease alone does not survive revocation.
// Identity arguments must come from authenticated middleware, never the body.
func validateReleaseObserverTask(task *models.AutomationTask, lease gatewayLease, gateway gatewayIdentity, agent authenticatedAgentCallback, runID string, now time.Time) error {
	return validateGatewayTaskReadAuthority(task, lease, gateway, agent, runID, now, agentwork.RoleReleaseManager, agentwork.LaneRelease, "delivery.release_gate")
}

func validateQASourceTask(task *models.AutomationTask, lease gatewayLease, gateway gatewayIdentity, agent authenticatedAgentCallback, runID string, now time.Time) error {
	return validateGatewayTaskReadAuthority(task, lease, gateway, agent, runID, now, agentwork.RoleQA, agentwork.LaneQA, "delivery.qa")
}

// Callers must load the task and active enrolled instance from the database
// both before and after external source reads. This check grants no URL,
// repository, publication, or model authority supplied by a worker.
func validateGatewayTaskReadAuthority(task *models.AutomationTask, lease gatewayLease, gateway gatewayIdentity, agent authenticatedAgentCallback, runID string, now time.Time, role agentwork.Role, lane agentwork.Lane, operation string) error {
	deny := func() error { return fmt.Errorf("release observation does not own an active exact task") }
	if task == nil || now.IsZero() || gateway.Role != role || gateway.Lane != lane ||
		lease.Version != 1 || lease.Role != string(gateway.Role) || lease.Lane != string(gateway.Lane) || lease.ExpiresAt <= now.Unix() || lease.ReceiptHandle == "" {
		return deny()
	}
	parsedRun, err := uuid.FromString(runID)
	if err != nil || parsedRun == uuid.Nil || task.ID == uuid.Nil || task.ID.String() != lease.TaskID || task.InputRef == "" || task.InputRef != lease.InputRef ||
		task.Operation != operation || task.Status != "running" || task.RunID != runID || task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.After(now) || task.CompletedAt != nil ||
		task.DeliveryWorkItemID == nil || *task.DeliveryWorkItemID == uuid.Nil || !artifactDigestPattern.MatchString(task.EvidenceSubjectDigest) {
		return deny()
	}
	if agent.InstanceID == uuid.Nil || agent.AgentKey == "" || agent.MachineID == "" || task.AgentInstanceID == nil || *task.AgentInstanceID != agent.InstanceID || task.AgentKey != agent.AgentKey || task.MachineID != agent.MachineID {
		return deny()
	}
	return nil
}
