package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/internal/releasegate"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// GatewayReleaseObservation accepts no candidate, URL or repository from a
// worker. It reads the server's immutable input under both a queue lease and
// the enrolled instance's live run. GitHub credentials stay on this server.
func GatewayReleaseObservation(c echo.Context) error {
	actor, ok := requireAgentCallbackIdentity(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	var request struct {
		LeaseToken string `json:"lease_token"`
		RunID      string `json:"run_id"`
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	decoder := json.NewDecoder(c.Request().Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return utils.Error(c, http.StatusBadRequest, "Invalid observation request", "")
	}
	lease, err := openGatewayLease(request.LeaseToken, identity)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid lease", "")
	}
	id, err := uuid.FromString(lease.TaskID)
	if err != nil || configuration.DB == nil {
		return utils.Error(c, http.StatusForbidden, "Observation unavailable", "")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 60*time.Second)
	defer cancel()
	var task models.AutomationTask
	load := func() error {
		if err := configuration.DB.WithContext(ctx).First(&task, id).Error; err != nil {
			return err
		}
		return validateReleaseObserverTask(&task, lease, identity, actor, request.RunID, time.Now().UTC())
	}
	if err := load(); err != nil {
		return utils.Error(c, http.StatusForbidden, "Observation denied", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	bucket, key, valid := validateGatewayObject(lease, cfg, lease.InputRef, false)
	if !valid {
		return utils.Error(c, http.StatusForbidden, "Observation input denied", "")
	}
	client, err := gatewayObjectClient(ctx, cfg, bucket)
	if err != nil || client == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Observation storage unavailable", "")
	}
	object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Observation input unavailable", "")
	}
	defer object.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(object.Body, gatewayMaxObjectBytes+1))
	if err != nil || len(raw) > gatewayMaxObjectBytes {
		return utils.Error(c, http.StatusBadRequest, "Observation input invalid", "")
	}
	var input automationagent.TaskInput
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&input); err != nil {
		return utils.Error(c, http.StatusBadRequest, "Observation input invalid", "")
	}
	candidate, err := automationagent.RunReleaseGate(input.Delivery)
	if err != nil {
		return utils.Error(c, http.StatusBadRequest, "Observation candidate invalid", "")
	}
	digest, err := releasegate.RevisionMatrixDigest(candidate.Revisions)
	if err != nil || digest != task.EvidenceSubjectDigest || candidate.ChangeSetID != task.DeliveryWorkItemID.String() {
		return utils.Error(c, http.StatusForbidden, "Observation subject denied", "")
	}
	observed, err := automationagent.RunReleaseGateWithGitHub(ctx, input.Delivery, os.Getenv)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Release evidence unavailable", "")
	}
	observedDigest, err := releasegate.RevisionMatrixDigest(observed.Revisions)
	if err != nil || observedDigest != digest {
		return utils.Error(c, http.StatusConflict, "Published revision changed", "")
	}
	environment, err := automationagent.RunReleaseEnvironmentWithGitHub(ctx, input.Delivery, observed, task.ID.String(), os.Getenv)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Release environment unavailable", "")
	}
	if err := load(); err != nil || task.EvidenceSubjectDigest != digest {
		return utils.Error(c, http.StatusForbidden, "Observation authority expired", "")
	}
	return utils.Success(c, http.StatusOK, "Release observed", map[string]any{"schema_version": 2, "gatekeeper_input": observed, "environment_observation": environment})
}
