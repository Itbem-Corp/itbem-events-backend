package automationagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"events-stocks/internal/agentwork"
	"events-stocks/internal/environmentevidence"
	"events-stocks/internal/releasegate"
)

func validateReleaseObservation(taskID string, delivery json.RawMessage, raw json.RawMessage) (map[string]any, error) {
	candidate, err := RunReleaseGate(delivery)
	if err != nil {
		return nil, err
	}
	want, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Input         json.RawMessage `json:"gatekeeper_input"`
		Environment   json.RawMessage `json:"environment_observation"`
	}
	if len(raw) == 0 || len(raw) > maxReleaseGateInputBytes {
		return nil, fmt.Errorf("release observation exceeds its boundary")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.SchemaVersion != 2 {
		return nil, fmt.Errorf("release observation schema is invalid")
	}
	observed, err := releasegate.DecodeInput(envelope.Input)
	if err != nil {
		return nil, err
	}
	got, err := releasegate.RevisionMatrixDigest(observed.Revisions)
	if err != nil || got != want || observed.Action != releasegate.ActionRelease || observed.ChangeSetID != candidate.ChangeSetID || observed.HumanApproval != nil {
		return nil, fmt.Errorf("release observation subject changed")
	}
	environment, err := environmentevidence.Decode(envelope.Environment)
	if err != nil || environment.TaskID != taskID || environment.MatrixDigest != want {
		return nil, fmt.Errorf("release environment subject changed")
	}
	if len(environment.Repositories) != len(candidate.Revisions) {
		return nil, fmt.Errorf("release environment matrix incomplete")
	}
	for _, repository := range environment.Repositories {
		matched := false
		for _, revision := range candidate.Revisions {
			if strings.EqualFold(repository.Repository, revision.Repository) && strings.EqualFold(repository.HeadSHA, revision.SHA) {
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("release environment revision changed")
		}
	}
	return releaseGateHandoff(observed, environment), nil
}

// Uses the enrolled instance signer and the gateway's lane token. It never
// accepts GitHub credentials or sends a candidate supplied by the worker.
func (c *HTTPCallback) ObserveRelease(ctx context.Context, gateway *HTTPGateway, taskID, runID string, delivery json.RawMessage) (map[string]any, error) {
	if c == nil || gateway == nil || gateway.role != agentwork.RoleReleaseManager || gateway.lane != agentwork.LaneRelease || gateway.baseURL != c.baseURL {
		return nil, fmt.Errorf("release observer requires its exact gateway")
	}
	lease, err := gatewayLeaseFromContext(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]string{"lease_token": lease, "run_id": runID})
	if err != nil {
		return nil, err
	}
	request, err := c.newSignedRequest(ctx, http.MethodPost, c.baseURL+"/api/internal/automation/gateway/release-observation", body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-Gateway-Token", gateway.token)
	request.Header.Set("X-Agent-Role", string(gateway.role))
	request.Header.Set("X-Agent-Lane", string(gateway.lane))
	client := *c.client
	client.Timeout = 90 * time.Second
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("release observation transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &gatewayRequestError{statusCode: response.StatusCode, operation: "release observation"}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxReleaseGateInputBytes+1))
	if err != nil || len(raw) > maxReleaseGateInputBytes {
		return nil, fmt.Errorf("release observation response exceeds its boundary")
	}
	var result struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("release observation response invalid")
	}
	return validateReleaseObservation(taskID, delivery, result.Data)
}
