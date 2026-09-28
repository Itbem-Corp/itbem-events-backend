package automationagent

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gofrs/uuid"
)

const gatewayAgentInstanceEnrollmentPath = "/api/internal/automation/agent-instances/enroll"

type gatewayAgentInstanceEnrollmentRequest struct {
	AgentKey  string `json:"agent_key"`
	MachineID string `json:"machine_id"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

type gatewayAgentInstanceEnrollmentResponse struct {
	Data struct {
		Instance struct {
			ID string `json:"id"`
		} `json:"instance"`
	} `json:"data"`
}

// EnrollAgentInstance uses the already-required role/lane gateway token and a
// signature from the protected local machine key. The API issues the opaque
// instance ID; callers never choose or send an ID.
func (g *HTTPGateway) EnrollAgentInstance(ctx context.Context, agentKey string, identity MachineIdentity) (string, error) {
	agentKey = strings.TrimSpace(agentKey)
	if !runtimeAgentKeyPattern.MatchString(agentKey) {
		return "", errors.New("agent enrollment requires a stable profile key")
	}
	publicKey, signature, err := identity.AgentInstanceEnrollmentProof(agentKey)
	if err != nil {
		return "", err
	}
	request := gatewayAgentInstanceEnrollmentRequest{AgentKey: agentKey, MachineID: identity.MachineID(), PublicKey: publicKey, Signature: signature}
	var response gatewayAgentInstanceEnrollmentResponse
	if err := g.request(ctx, http.MethodPost, gatewayAgentInstanceEnrollmentPath, request, &response); err != nil {
		return "", err
	}
	instanceID := strings.TrimSpace(response.Data.Instance.ID)
	parsed, err := uuid.FromString(instanceID)
	if err != nil || parsed == uuid.Nil || parsed.String() != instanceID {
		return "", errors.New("agent enrollment response did not contain a valid instance ID")
	}
	return parsed.String(), nil
}
