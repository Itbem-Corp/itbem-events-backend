package automation

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
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
	"github.com/gofrs/uuid"
	"github.com/labstack/echo/v4"
)

const (
	// SQS visibility still renews in bounded intervals. This upper bound only
	// prevents a sealed lease copied from worker memory becoming permanent.
	gatewayLeaseLifetime  = 13 * time.Hour
	gatewayMaxObjectBytes = 10 << 20
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
	if !automationqueue.IsConfigured() || configuration.GetS3Client(nil) == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Gateway unavailable", "")
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
	if !write {
		return bucket, key, subtle.ConstantTimeCompare([]byte(reference), []byte(lease.InputRef)) == 1 && inputReferenceMatches(cfg, reference)
	}
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
	client := configuration.GetS3Client(nil)
	if client == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	response, err := client.GetObject(c.Request().Context(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, gatewayMaxObjectBytes+1))
	if err != nil || len(body) > gatewayMaxObjectBytes {
		return utils.Error(c, http.StatusRequestEntityTooLarge, "Object unavailable", "")
	}
	return utils.Success(c, http.StatusOK, "Automation object read", map[string]any{"body": base64.StdEncoding.EncodeToString(body)})
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
	client := configuration.GetS3Client(nil)
	if client == nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	_, err = client.PutObject(c.Request().Context(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))), ContentType: aws.String(contentType), ServerSideEncryption: s3types.ServerSideEncryptionAes256})
	if err != nil {
		return utils.Error(c, http.StatusServiceUnavailable, "Storage unavailable", "")
	}
	return c.NoContent(http.StatusNoContent)
}
