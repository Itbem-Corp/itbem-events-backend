package automation

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	"events-stocks/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

// Signed source requests carry only a workspace selector, queue lease and
// live run. Repository, commit and matrix come from immutable server input.
func GatewayQASource(c echo.Context) error {
	return gatewayQASourceWithAcquirer(c, func(ctx context.Context, subject qaSourceSubject) ([]byte, string, error) {
		app, err := automationagent.LoadGitHubSourceAppConfig(os.Getenv)
		if err != nil {
			return nil, "", err
		}
		return automationagent.FetchGitHubQASourcePack(ctx, subject.Repository, subject.SHA, app, nil)
	})
}

// The route always supplies the server's bounded GitHub acquisition above.
// An explicit dependency lets isolated database fixtures exercise revocation
// without granting a production URL or credential override.
func gatewayQASourceWithAcquirer(c echo.Context, acquire func(context.Context, qaSourceSubject) ([]byte, string, error)) error {
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
		Reference  string `json:"repository_ref"`
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	decoder := json.NewDecoder(c.Request().Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return utils.Error(c, http.StatusBadRequest, "Invalid source request", "")
	}
	lease, err := openGatewayLease(request.LeaseToken, identity)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid lease", "")
	}
	id, err := uuid.FromString(lease.TaskID)
	if err != nil || configuration.DB == nil {
		return utils.Error(c, http.StatusForbidden, "Source unavailable", "")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 90*time.Second)
	defer cancel()
	var task models.AutomationTask
	load := func() error {
		var instance models.AutomationAgentInstance
		if err := configuration.DB.WithContext(ctx).Where("id = ? AND status = ?", actor.InstanceID, "active").First(&instance).Error; err != nil {
			return err
		}
		if instance.AgentKey != actor.AgentKey || instance.MachineID != actor.MachineID {
			return fmt.Errorf("QA source instance changed")
		}
		if err := configuration.DB.WithContext(ctx).First(&task, id).Error; err != nil {
			return err
		}
		return validateQASourceTask(&task, lease, identity, actor, request.RunID, time.Now().UTC())
	}
	if load() != nil {
		return utils.Error(c, http.StatusForbidden, "Source authority denied", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	bucket, key, valid := validateGatewayObject(lease, cfg, lease.InputRef, false)
	if !valid {
		return utils.Error(c, http.StatusForbidden, "Source input denied", "")
	}
	storage, err := gatewayObjectClient(ctx, cfg, bucket)
	if err != nil || storage == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Source input unavailable", "")
	}
	object, err := storage.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Source input unavailable", "")
	}
	defer object.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(object.Body, gatewayMaxObjectBytes+1))
	if err != nil || len(raw) > gatewayMaxObjectBytes {
		return utils.Error(c, http.StatusBadRequest, "Source input invalid", "")
	}
	var input automationagent.TaskInput
	if json.Unmarshal(raw, &input) != nil {
		return utils.Error(c, http.StatusBadRequest, "Source input invalid", "")
	}
	subject, err := qaSourceSubjectForTask(&task, input.Delivery, request.Reference)
	if err != nil {
		return utils.Error(c, http.StatusForbidden, "Source subject denied", "")
	}
	matrix := task.EvidenceSubjectDigest
	pack, digest, err := acquire(ctx, subject)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Bounded source acquisition unavailable", "")
	}
	if len(pack) < 12 || len(pack) > 64<<20 || string(pack[:4]) != "PACK" || fmt.Sprintf("%x", sha256.Sum256(pack)) != digest {
		return utils.Error(c, http.StatusServiceUnavailable, "Source package integrity invalid", "")
	}
	if load() != nil || task.EvidenceSubjectDigest != matrix {
		return utils.Error(c, http.StatusForbidden, "Source authority expired", "")
	}
	confirmed, err := qaSourceSubjectForTask(&task, input.Delivery, request.Reference)
	if err != nil || confirmed != subject {
		return utils.Error(c, http.StatusForbidden, "Source subject changed", "")
	}
	if err := recordServerQASourceReceipt(configuration.DB.WithContext(ctx), &task, lease, identity, actor, subject, digest, int64(len(pack)), time.Now().UTC()); err != nil {
		return utils.Error(c, http.StatusConflict, "Source receipt could not be sealed", "")
	}
	metadata, err := json.Marshal(map[string]any{"schema_version": 1, "task_id": task.ID.String(), "run_id": request.RunID, "matrix_digest": matrix, "repository_ref": subject.Reference, "repository": subject.Repository, "branch": subject.Branch, "commit_sha": subject.SHA, "pack_sha256": digest})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Source metadata unavailable", "")
	}
	c.Response().Header().Set("X-ITBEM-QA-Source", base64.RawURLEncoding.EncodeToString(metadata))
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "application/x-git-packed-objects", pack)
}
