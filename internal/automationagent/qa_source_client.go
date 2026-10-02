package automationagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"events-stocks/internal/agentwork"
	"events-stocks/internal/releasegate"
)

type QASourceMetadata struct {
	SchemaVersion int    `json:"schema_version"`
	TaskID        string `json:"task_id"`
	RunID         string `json:"run_id"`
	MatrixDigest  string `json:"matrix_digest"`
	Reference     string `json:"repository_ref"`
	Repository    string `json:"repository"`
	Branch        string `json:"branch"`
	CommitSHA     string `json:"commit_sha"`
	PackSHA256    string `json:"pack_sha256"`
	BundleSHA256  string `json:"bundle_sha256,omitempty"`
}

func validateQASourceMetadata(raw []byte, taskID, runID, reference string, delivery json.RawMessage, workspace Workspace) (QASourceMetadata, error) {
	deny := func() (QASourceMetadata, error) {
		return QASourceMetadata{}, fmt.Errorf("QA source response differs from its frozen subject")
	}
	var metadata QASourceMetadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 4096 || decoder.Decode(&metadata) != nil || decoder.Decode(&struct{}{}) != io.EOF || (metadata.SchemaVersion != 1 && metadata.SchemaVersion != 2) || metadata.TaskID != taskID || metadata.RunID != runID || metadata.Reference != reference || !sha256DigestPattern.MatchString(metadata.PackSHA256) {
		return deny()
	}
	if (metadata.SchemaVersion == 1 && metadata.BundleSHA256 != "") || (metadata.SchemaVersion == 2 && !sha256DigestPattern.MatchString(metadata.BundleSHA256)) {
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
	if err != nil {
		return deny()
	}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil || digest != metadata.MatrixDigest {
		return deny()
	}
	repository, _, required, err := gitHubSourceWorkspaceRemote(workspace)
	if err != nil || !required || !strings.EqualFold(repository.Owner+"/"+repository.Name, metadata.Repository) {
		return deny()
	}
	matches := 0
	for _, change := range input.Changes {
		if change.Reference != reference || change.Branch != metadata.Branch || change.SHA != metadata.CommitSHA || change.ReviewType != "pull_request" || change.CIStatus != "passed" || !strings.EqualFold(change.Metadata.Repository, metadata.Repository) {
			continue
		}
		for _, revision := range candidate.Revisions {
			if strings.EqualFold(revision.Repository, metadata.Repository) && revision.Branch == change.Metadata.TargetBranch && revision.SHA == metadata.CommitSHA {
				matches++
			}
		}
	}
	if matches != 1 {
		return deny()
	}
	return metadata, nil
}

func (c *HTTPCallback) AcquireQASource(ctx context.Context, gateway *HTTPGateway, taskID, runID, reference string, delivery json.RawMessage, lookup func(string) string) (string, error) {
	if c == nil || gateway == nil || gateway.role != agentwork.RoleQA || gateway.lane != agentwork.LaneQA || gateway.baseURL != c.baseURL {
		return "", fmt.Errorf("QA source requires its exact gateway")
	}
	workspace, err := RegisteredWorkspace(reference, lookup)
	if err != nil {
		return "", err
	}
	lease, err := gatewayLeaseFromContext(ctx)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]string{"lease_token": lease, "run_id": runID, "repository_ref": reference})
	if err != nil {
		return "", err
	}
	request, err := c.newSignedRequest(ctx, http.MethodPost, c.baseURL+"/api/internal/automation/gateway/qa-source", body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-Gateway-Token", gateway.token)
	request.Header.Set("X-Agent-Role", string(gateway.role))
	request.Header.Set("X-Agent-Lane", string(gateway.lane))
	client := *c.client
	client.Timeout = 120 * time.Second
	response, err := client.Do(request)
	if err != nil {
		return "", &RetryableError{Message: "QA source transport unavailable", RetryAfter: time.Minute}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", &gatewayRequestError{statusCode: response.StatusCode, operation: "QA source acquisition"}
	}
	header := response.Header.Get("X-ITBEM-QA-Source")
	contentType := response.Header.Get("Content-Type")
	if len(header) > 8192 || (contentType != "application/x-git-packed-objects" && contentType != "application/vnd.itbem.qa-source-bundle") {
		return "", fmt.Errorf("QA source response boundary invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return "", fmt.Errorf("QA source metadata encoding invalid")
	}
	metadata, err := validateQASourceMetadata(raw, taskID, runID, reference, delivery, workspace)
	if err != nil {
		return "", err
	}
	if metadata.SchemaVersion == 2 {
		if contentType != "application/vnd.itbem.qa-source-bundle" {
			return "", fmt.Errorf("QA bundle transport type differs from metadata")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, MaxQASourceBundleBytes+1))
		if err != nil || len(body) > MaxQASourceBundleBytes {
			return "", fmt.Errorf("QA bundle response exceeds its boundary")
		}
		bundle, err := DecodeQASourceBundle(body, metadata.BundleSHA256)
		if err != nil || bundle.PackSHA256 != metadata.PackSHA256 {
			return "", fmt.Errorf("QA bundle does not match authenticated source metadata")
		}
		children := make([]packedQASourceDependency, 0, len(bundle.Dependencies))
		for _, child := range bundle.Dependencies {
			if !strings.EqualFold(workspace.Config.QASourceDependencies[child.Path], child.Repository) {
				return "", fmt.Errorf("QA bundle dependency is not approved by this workspace")
			}
			children = append(children, packedQASourceDependency{pinnedQASourceDependency: pinnedQASourceDependency{Path: child.Path, Repository: child.Repository, CommitSHA: child.CommitSHA}, PackSHA256: child.PackSHA256, Pack: child.Pack})
		}
		return materializePublishedQASourceBundle(ctx, workspace, metadata.Branch, metadata.CommitSHA, bundle.PackSHA256, bundle.Pack, workspace.Config.QASourceDependencies, children)
	}
	if contentType != "application/x-git-packed-objects" {
		return "", fmt.Errorf("QA source transport type differs from metadata")
	}
	pack, err := io.ReadAll(io.LimitReader(response.Body, maxQASourcePackBytes+1))
	if err != nil || len(pack) > maxQASourcePackBytes {
		return "", fmt.Errorf("QA source response pack exceeds its boundary")
	}
	return materializePublishedQASourcePack(ctx, workspace, metadata.Branch, metadata.CommitSHA, metadata.PackSHA256, pack)
}
