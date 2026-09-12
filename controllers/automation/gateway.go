package automation

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"events-stocks/configuration"
	"events-stocks/internal/agentwork"
	"events-stocks/internal/automationagent"
	"events-stocks/models"
	automationqueue "events-stocks/repositories/automationqueuerepository"
	"events-stocks/utils"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const (
	// SQS visibility still renews in bounded intervals. This upper bound only
	// prevents a sealed lease copied from worker memory becoming permanent.
	gatewayLeaseLifetime  = 13 * time.Hour
	gatewayMaxObjectBytes = 10 << 20
	// Keep review-lease repair below the agent's gateway timeout. The normal
	// lease call also long-polls SQS, so an unbounded GitHub/S3 repair here can
	// make a healthy reviewer time out before it receives queued work.
	gatewayReviewLeaseReconciliationTimeout = 5 * time.Second
	// gatewayStorageFailureHeader is deliberately a small, stable diagnostic
	// surface. It lets a locally operated worker distinguish a recoverable
	// control-plane storage failure from a broken task without disclosing an
	// object key, bucket, AWS request id, credential, or provider response.
	gatewayStorageFailureHeader = "X-ITBEM-Gateway-Storage-Failure"
	gatewayObjectClientTimeout  = 10 * time.Second
)

type gatewayIdentity struct {
	Role agentwork.Role
	Lane agentwork.Lane
}

type gatewayLease struct {
	Version       int    `json:"v"`
	Role          string `json:"role"`
	Lane          string `json:"lane"`
	TaskID        string `json:"task_id"`
	InputRef      string `json:"input_ref"`
	ReceiptHandle string `json:"receipt_handle"`
	ExpiresAt     int64  `json:"expires_at"`
}

type gatewayLeaseRequest struct {
	Limit int `json:"limit"`
}

type gatewayLeaseMessage struct {
	Body       string `json:"body"`
	LeaseToken string `json:"lease_token"`
}

// gatewayObjectClient is deliberately scoped to the validated target bucket.
// Media storage and private automation storage may be in separate AWS regions;
// using the media client's signing region here can make a valid sealed task
// look like a storage outage. The gateway still validates the lease, bucket
// and task prefix before this helper is reached.
func gatewayObjectClient(ctx context.Context, cfg *models.Config, bucket string) (*s3.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, gatewayObjectClientTimeout)
	defer cancel()
	client, _, err := configuration.BuildS3ClientForBucket(ctx, cfg, bucket)
	return client, err
}

type gatewayVisibilityRequest struct {
	LeaseToken string `json:"lease_token"`
	Seconds    int32  `json:"seconds"`
}

type gatewayObjectRequest struct {
	LeaseToken  string `json:"lease_token"`
	Reference   string `json:"reference"`
	Body        string `json:"body,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

func gatewayIdentityFromRequest(c echo.Context) (gatewayIdentity, bool) {
	identity := gatewayIdentity{Role: agentwork.Role(strings.TrimSpace(c.Request().Header.Get("X-Agent-Role"))), Lane: agentwork.Lane(strings.TrimSpace(c.Request().Header.Get("X-Agent-Lane")))}
	if !agentwork.IsKnownRoleLane(identity.Role, identity.Lane) {
		return gatewayIdentity{}, false
	}
	provided := strings.TrimSpace(c.Request().Header.Get("X-Agent-Gateway-Token"))
	if provided == "" {
		return gatewayIdentity{}, false
	}
	valid := 0
	for _, name := range []string{"AUTOMATION_CALLBACK_SECRET", "AUTOMATION_CALLBACK_SECRET_PREVIOUS"} {
		if root := strings.TrimSpace(os.Getenv(name)); root != "" {
			expected := deriveGatewayToken(root, identity)
			valid |= subtle.ConstantTimeCompare([]byte(provided), []byte(expected))
		}
	}
	return identity, valid == 1
}

func deriveGatewayToken(root string, identity gatewayIdentity) string {
	mac := hmac.New(sha256.New, []byte(root))
	_, _ = mac.Write([]byte("itbem-agent-gateway:v1:" + string(identity.Role) + ":" + string(identity.Lane)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func gatewayKey(root string) []byte {
	sum := sha256.Sum256([]byte("itbem-agent-lease:v1:" + root))
	return sum[:]
}

func sealGatewayLease(lease gatewayLease) (string, error) {
	root := strings.TrimSpace(os.Getenv("AUTOMATION_CALLBACK_SECRET"))
	if root == "" {
		return "", fmt.Errorf("gateway signing secret is unavailable")
	}
	body, err := json.Marshal(lease)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(gatewayKey(root))
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, body, []byte("itbem-agent-lease:v1"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openGatewayLease(token string, identity gatewayIdentity) (gatewayLease, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return gatewayLease{}, fmt.Errorf("invalid lease token")
	}
	for _, name := range []string{"AUTOMATION_CALLBACK_SECRET", "AUTOMATION_CALLBACK_SECRET_PREVIOUS"} {
		root := strings.TrimSpace(os.Getenv(name))
		if root == "" {
			continue
		}
		block, blockErr := aes.NewCipher(gatewayKey(root))
		if blockErr != nil {
			continue
		}
		aead, aeadErr := cipher.NewGCM(block)
		if aeadErr != nil || len(sealed) <= aead.NonceSize() {
			continue
		}
		body, openErr := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte("itbem-agent-lease:v1"))
		if openErr != nil {
			continue
		}
		var lease gatewayLease
		if json.Unmarshal(body, &lease) != nil || lease.Version != 1 || lease.Role != string(identity.Role) || lease.Lane != string(identity.Lane) || lease.ExpiresAt < time.Now().UTC().Unix() || strings.TrimSpace(lease.ReceiptHandle) == "" {
			continue
		}
		return lease, nil
	}
	return gatewayLease{}, fmt.Errorf("invalid or expired lease token")
}

func GatewayProbe(c echo.Context) error {
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	storage := configuration.GetS3Client(nil)
	if !automationqueue.IsConfigured() || storage == nil || cfg == nil || automationqueue.ProbeLane(c.Request().Context(), identity.Lane) != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Gateway unavailable", "")
	}
	for _, bucket := range []string{strings.TrimSpace(cfg.AutomationInputBucket), strings.TrimSpace(cfg.AutomationOutputBucket)} {
		if bucket == "" {
			return utils.Error(c, http.StatusServiceUnavailable, "Gateway unavailable", "")
		}
		if _, err := storage.GetBucketLocation(c.Request().Context(), &s3.GetBucketLocationInput{Bucket: aws.String(bucket)}); err != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Gateway unavailable", "")
		}
	}
	return utils.Success(c, http.StatusOK, "Agent gateway ready", map[string]any{"ready": true, "role": identity.Role, "lane": identity.Lane})
}

func GatewayLease(c echo.Context) error {
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 4<<10)
	request := gatewayLeaseRequest{Limit: 1}
	if c.Request().ContentLength != 0 {
		if err := c.Bind(&request); err != nil {
			return utils.Error(c, http.StatusBadRequest, "Invalid lease request", "")
		}
	}
	if request.Limit < 1 || request.Limit > 10 {
		return utils.Error(c, http.StatusBadRequest, "Invalid lease request", "")
	}
	if identity.Role == agentwork.RoleReviewer && identity.Lane == agentwork.LaneReview {
		// Lost worker leases are repaired only after revalidating the immutable
		// GitHub subject. Keep this best-effort maintenance separate from the
		// normal lease path: a transient GitHub or storage outage must never
		// prevent a healthy Reviewer from processing already-queued work. The
		// bounded child context leaves enough of the request budget for SQS's
		// long poll, while preserving best-effort expired-lease recovery.
		cfg, _ := c.Get("config").(*models.Config)
		reconcileCtx, cancel := context.WithTimeout(c.Request().Context(), gatewayReviewLeaseReconciliationTimeout)
		_, _ = reconcileOneExpiredGitHubReviewLease(reconcileCtx, cfg, time.Now().UTC())
		cancel()
	}
	messages, err := automationqueue.ReceiveLane(c.Request().Context(), identity.Lane, request.Limit)
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation queue unavailable", "")
	}
	result := make([]gatewayLeaseMessage, 0, len(messages))
	for _, raw := range messages {
		message, decodeErr := automationagent.DecodeTaskMessage(raw.Body)
		if decodeErr != nil {
			continue
		}
		assignment, assigned := agentwork.AssignmentForOperation(message.Payload.Operation)
		if !assigned || assignment.Role != identity.Role || assignment.Lane != identity.Lane {
			continue
		}
		lease := gatewayLease{Version: 1, Role: string(identity.Role), Lane: string(identity.Lane), TaskID: message.Payload.TaskID, InputRef: message.Payload.InputRef, ReceiptHandle: raw.ReceiptHandle, ExpiresAt: time.Now().UTC().Add(gatewayLeaseLifetime).Unix()}
		token, sealErr := sealGatewayLease(lease)
		if sealErr != nil {
			return utils.Error(c, http.StatusServiceUnavailable, "Automation gateway unavailable", "")
		}
		result = append(result, gatewayLeaseMessage{Body: raw.Body, LeaseToken: token})
	}
	return utils.Success(c, http.StatusOK, "Automation leases acquired", map[string]any{"messages": result})
}

func GatewayVisibility(c echo.Context) error {
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	var request gatewayVisibilityRequest
	if c.Bind(&request) != nil || request.Seconds < 1 || request.Seconds > 43_200 {
		return utils.Error(c, http.StatusBadRequest, "Invalid visibility request", "")
	}
	lease, err := openGatewayLease(request.LeaseToken, identity)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid lease", "")
	}
	if err := automationqueue.ChangeLaneVisibility(c.Request().Context(), identity.Lane, lease.ReceiptHandle, request.Seconds); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation queue unavailable", "")
	}
	return c.NoContent(http.StatusNoContent)
}

func GatewayAcknowledge(c echo.Context) error {
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	var request gatewayVisibilityRequest
	if c.Bind(&request) != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid acknowledgement", "")
	}
	lease, err := openGatewayLease(request.LeaseToken, identity)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid lease", "")
	}
	if err := automationqueue.DeleteLaneMessage(c.Request().Context(), identity.Lane, lease.ReceiptHandle); err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Automation queue unavailable", "")
	}
	return c.NoContent(http.StatusNoContent)
}

func validateGatewayObject(lease gatewayLease, cfg *models.Config, reference string, write bool) (string, string, bool) {
	bucket, key, err := privateReference(reference)
	if err != nil {
		return "", "", false
	}
	if !write && subtle.ConstantTimeCompare([]byte(reference), []byte(lease.InputRef)) == 1 && inputReferenceMatches(cfg, reference) {
		return bucket, key, true
	}
	// A worker may resume or deduplicate work only from evidence already scoped
	// to the exact task in its sealed lease.  In particular, code review reads
	// its checkpoint before it can decide whether to call the provider again.
	// Keep the input immutable and exact, while allowing neither reads nor
	// writes to escape this task's private output namespace.
	taskID, err := uuid.FromString(lease.TaskID)
	if err != nil || cfg == nil || subtle.ConstantTimeCompare([]byte(bucket), []byte(strings.TrimSpace(cfg.AutomationOutputBucket))) != 1 {
		return "", "", false
	}
	prefix := "automation/" + taskID.String() + "/"
	return bucket, key, strings.HasPrefix(key, prefix) && !strings.Contains(key, "..")
}

func GatewayReadObject(c echo.Context) error {
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, 16<<10)
	var request gatewayObjectRequest
	if c.Bind(&request) != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid object request", "")
	}
	lease, err := openGatewayLease(request.LeaseToken, identity)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid lease", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	bucket, key, valid := validateGatewayObject(lease, cfg, request.Reference, false)
	if !valid {
		return utils.Error(c, http.StatusForbidden, "Object outside task lease", "")
	}
	client, err := gatewayObjectClient(c.Request().Context(), cfg, bucket)
	if err != nil || client == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	response, err := client.GetObject(c.Request().Context(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		if gatewayObjectMissing(err) {
			return utils.Error(c, http.StatusNotFound, "Object not found", "")
		}
		c.Response().Header().Set(gatewayStorageFailureHeader, gatewayStorageFailureCode(err))
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, gatewayMaxObjectBytes+1))
	if err != nil || len(body) > gatewayMaxObjectBytes {
		return utils.Error(c, http.StatusRequestEntityTooLarge, "Object unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation object read", map[string]any{"body": base64.StdEncoding.EncodeToString(body)})
}

func gatewayObjectMissing(err error) bool {
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(strings.TrimSpace(apiErr.ErrorCode())) {
		case "notfound", "nosuchkey", "nosuchobject":
			return true
		}
	}
	var statusErr interface{ HTTPStatusCode() int }
	return errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusNotFound
}

// gatewayStorageFailureCode normalizes only operator-actionable storage error
// classes. It must never return a raw SDK error because this response is
// consumed outside the trusted backend process.
func gatewayStorageFailureCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(strings.TrimSpace(apiErr.ErrorCode())) {
		case "accessdenied", "invalidaccesskeyid", "signaturedoesnotmatch", "expiredtoken":
			return "authorization"
		case "authorizationheadermalformed", "permanentredirect", "incorrectendpoint":
			return "region"
		case "requesttimeout", "slowdown", "serviceunavailable", "internalerror":
			return "transient"
		}
	}
	var statusErr interface{ HTTPStatusCode() int }
	if errors.As(err, &statusErr) && statusErr.HTTPStatusCode() >= http.StatusInternalServerError {
		return "transient"
	}
	return "unclassified"
}

func GatewayWriteObject(c echo.Context) error {
	identity, ok := gatewayIdentityFromRequest(c)
	if !ok {
		return utils.Error(c, http.StatusUnauthorized, "Unauthorized", "")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, gatewayMaxObjectBytes*2)
	var request gatewayObjectRequest
	if c.Bind(&request) != nil {
		return utils.Error(c, http.StatusBadRequest, "Invalid object request", "")
	}
	lease, err := openGatewayLease(request.LeaseToken, identity)
	if err != nil {
		return utils.Error(c, http.StatusUnauthorized, "Invalid lease", "")
	}
	cfg, _ := c.Get("config").(*models.Config)
	bucket, key, valid := validateGatewayObject(lease, cfg, request.Reference, true)
	if !valid {
		return utils.Error(c, http.StatusForbidden, "Object outside task lease", "")
	}
	body, err := base64.StdEncoding.DecodeString(request.Body)
	if err != nil || len(body) > gatewayMaxObjectBytes {
		return utils.Error(c, http.StatusRequestEntityTooLarge, "Invalid object", "")
	}
	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" || len(contentType) > 128 || strings.ContainsAny(contentType, "\r\n") {
		contentType = "application/octet-stream"
	}
	client, err := gatewayObjectClient(c.Request().Context(), cfg, bucket)
	if err != nil || client == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	_, err = client.PutObject(c.Request().Context(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))), ContentType: aws.String(contentType), ServerSideEncryption: s3types.ServerSideEncryptionAes256})
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	return c.NoContent(http.StatusNoContent)
}
