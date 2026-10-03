package automation

import (
	"strings"
	"testing"
	"time"

	"events-stocks/internal/agentwork"
	"events-stocks/models"
	"github.com/gofrs/uuid"
)

func TestReleaseObserverAdmissionRequiresExactLiveTaskAndSignedInstance(t *testing.T) {
	testGatewayTaskReadAdmission(t, agentwork.RoleReleaseManager, agentwork.LaneRelease, "delivery.release_gate", validateReleaseObserverTask)
}

func TestQASourceAdmissionRequiresExactLiveTaskAndSignedInstance(t *testing.T) {
	testGatewayTaskReadAdmission(t, agentwork.RoleQA, agentwork.LaneQA, "delivery.qa", validateQASourceTask)
}

func testGatewayTaskReadAdmission(t *testing.T, role agentwork.Role, lane agentwork.Lane, operation string, validate func(*models.AutomationTask, gatewayLease, gatewayIdentity, authenticatedAgentCallback, string, time.Time) error) {
	t.Helper()
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	id, instance, item, run := uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()), uuid.Must(uuid.NewV4()).String()
	task := models.AutomationTask{ID: id, Operation: operation, Status: "running", RunID: run, LeaseExpiresAt: &expires, InputRef: "s3://synthetic/task.json", AgentInstanceID: &instance, AgentKey: "synthetic-agent", MachineID: "synthetic-host", DeliveryWorkItemID: &item, EvidenceSubjectDigest: strings.Repeat("a", 64)}
	identity := gatewayIdentity{Role: role, Lane: lane}
	agent := authenticatedAgentCallback{InstanceID: instance, AgentKey: task.AgentKey, MachineID: task.MachineID}
	lease := gatewayLease{Version: 1, Role: string(identity.Role), Lane: string(identity.Lane), TaskID: id.String(), InputRef: task.InputRef, ExpiresAt: expires.Unix(), ReceiptHandle: "synthetic-receipt"}
	if err := validate(&task, lease, identity, agent, run, now); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"cancelled", "completed", "expired-task", "expired-queue", "other-task", "other-input", "other-run", "other-instance", "other-machine", "other-agent", "wrong-role", "wrong-operation", "missing-subject"} {
		t.Run(scenario, func(t *testing.T) {
			candidate, queue, actor, gateway := task, lease, agent, identity
			switch scenario {
			case "cancelled":
				candidate.Status = "cancelled"
			case "completed":
				candidate.CompletedAt = &now
			case "expired-task":
				candidate.LeaseExpiresAt = &now
			case "expired-queue":
				queue.ExpiresAt = now.Unix()
			case "other-task":
				queue.TaskID = uuid.Must(uuid.NewV4()).String()
			case "other-input":
				queue.InputRef = "s3://synthetic/other.json"
			case "other-run":
				candidate.RunID = uuid.Must(uuid.NewV4()).String()
			case "other-instance":
				actor.InstanceID = uuid.Must(uuid.NewV4())
			case "other-machine":
				actor.MachineID = "other"
			case "other-agent":
				actor.AgentKey = "other"
			case "wrong-role":
				gateway.Role = agentwork.RolePrincipalEngineer
			case "wrong-operation":
				candidate.Operation = "delivery.publish"
			case "missing-subject":
				candidate.EvidenceSubjectDigest = ""
			}
			if err := validate(&candidate, queue, gateway, actor, run, now); err == nil {
				t.Fatal("unauthorized observation admitted")
			}
		})
	}
}
