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
	return gatewayQASourceWithBundleAcquirer(c, func(ctx context.Context, subject qaSourceSubject) (qaSourceAcquisition, error) {
		policy, err := automationagent.LoadQASourceDependencyPolicy(os.Getenv, subject.Repository)
		if err != nil {
			return qaSourceAcquisition{}, err
		}
		app, err := automationagent.LoadGitHubSourceAppConfig(os.Getenv)
		if err != nil {
			return qaSourceAcquisition{}, err
		}
		bundle, err := automationagent.FetchGitHubQASourceBundle(ctx, subject.Repository, subject.SHA, policy, app, nil)
		if err != nil {
			return qaSourceAcquisition{}, err
		}
		return qaSourceBundleAcquisition(bundle)
	})
}

type qaSourceAcquisition struct {
	pack         []byte
	digest       string
	body         []byte
	bundleDigest string
	dependencies string
}

func qaSourceBundleAcquisition(bundle automationagent.QASourceBundle) (qaSourceAcquisition, error) {
	body, digest, err := automationagent.EncodeQASourceBundle(bundle)
	if err != nil {
		return qaSourceAcquisition{}, err
	}
	decoded, err := automationagent.DecodeQASourceBundle(body, digest)
	if err != nil {
		return qaSourceAcquisition{}, err
	}
	descriptors := make([]map[string]any, 0, len(decoded.Dependencies))
	for _, child := range decoded.Dependencies {
		descriptors = append(descriptors, map[string]any{"path": child.Path, "repository": child.Repository, "commit_sha": child.CommitSHA, "pack_sha256": child.PackSHA256, "pack_bytes": len(child.Pack)})
	}
	raw, err := json.Marshal(descriptors)
	if err != nil {
		return qaSourceAcquisition{}, err
	}
	return qaSourceAcquisition{pack: bundle.Pack, digest: bundle.PackSHA256, body: body, bundleDigest: digest, dependencies: string(raw)}, nil
}

// The route always supplies the server's bounded GitHub acquisition above.
// An explicit dependency lets isolated database fixtures exercise revocation
// without granting a production URL or credential override.
func gatewayQASourceWithAcquirer(c echo.Context, acquire func(context.Context, qaSourceSubject) ([]byte, string, error)) error {
	return gatewayQASourceWithBundleAcquirer(c, func(ctx context.Context, subject qaSourceSubject) (qaSourceAcquisition, error) {
		pack, digest, err := acquire(ctx, subject)
		return qaSourceAcquisition{pack: pack, digest: digest, body: pack}, err
	})
}

func gatewayQASourceWithBundleAcquirer(c echo.Context, acquire func(context.Context, qaSourceSubject) (qaSourceAcquisition, error)) error {
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
	acquired, err := acquire(ctx, subject)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Bounded source acquisition unavailable", "")
	}
	pack, digest := acquired.pack, acquired.digest
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
	var bundleBytes int64
	version, contentType := 1, "application/x-git-packed-objects"
	if acquired.bundleDigest != "" {
		decoded, err := automationagent.DecodeQASourceBundle(acquired.body, acquired.bundleDigest)
		if err != nil || decoded.PackSHA256 != digest {
			return utils.Error(c, http.StatusServiceUnavailable, "Source bundle integrity invalid", "")
		}
		bundleBytes = int64(len(acquired.body))
		version, contentType = 2, "application/vnd.itbem.qa-source-bundle"
	}
	if err := recordServerQASourceBundleReceipt(configuration.DB.WithContext(ctx), &task, lease, identity, actor, subject, digest, int64(len(pack)), acquired.bundleDigest, bundleBytes, acquired.dependencies, time.Now().UTC()); err != nil {
		return utils.Error(c, http.StatusConflict, "Source receipt could not be sealed", "")
	}
	metadata, err := json.Marshal(automationagent.QASourceMetadata{SchemaVersion: version, TaskID: task.ID.String(), RunID: request.RunID, MatrixDigest: matrix, Reference: subject.Reference, Repository: subject.Repository, Branch: subject.Branch, CommitSHA: subject.SHA, PackSHA256: digest, BundleSHA256: acquired.bundleDigest})
	if err != nil {
		return utils.Error(c, http.StatusInternalServerError, "Source metadata unavailable", "")
	}
	c.Response().Header().Set("X-ITBEM-QA-Source", base64.RawURLEncoding.EncodeToString(metadata))
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, contentType, acquired.body)
}
