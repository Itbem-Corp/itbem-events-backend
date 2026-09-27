// Package inferencecapability defines short-lived, server-signed bearer
// capabilities for local agents and pinned tools using the cloud inference
// gateway. Signing keys are control-plane-only and must never be sent to a
// local worker.
package inferencecapability

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const (
	HeaderName          = "X-ITBEM-Inference-Capability"
	OperationDeliveryQA = "delivery.qa"
	capabilityVersion   = 2
	capabilityAudience  = "itbem/automation/inference"
	maxPayloadBytes     = 1024
	maxCapabilityTTL    = 5 * time.Minute
	maxClockSkew        = 30 * time.Second
)

// MaxTTL is the hard upper bound accepted by both the signer and verifier.
const MaxTTL = maxCapabilityTTL

var agentKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

// Scope is fully authenticated by the signed token. Infer still compares the
// worker identity and current run against the live database lease on every call.
type Scope struct {
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	Operation string `json:"operation"`
	WorkerID  string `json:"worker_id"`
	AgentKey  string `json:"agent_key"`
	MachineID string `json:"machine_id"`
}

type claims struct {
	Version   int    `json:"v"`
	Audience  string `json:"aud"`
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	Operation string `json:"operation"`
	WorkerID  string `json:"worker_id"`
	AgentKey  string `json:"agent_key"`
	MachineID string `json:"machine_id"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// Mint signs a capability for one claimed worker run. signingKey must be a
// server-only key unrelated to the callback master present on local machines.
func Mint(signingKey string, scope Scope, ttl time.Duration) (string, error) {
	scope = normalizeScope(scope)
	if len(strings.TrimSpace(signingKey)) < 32 {
		return "", errors.New("inference capability signing key is invalid")
	}
	if !validScope(scope) {
		return "", errors.New("inference capability scope is invalid")
	}
	if ttl < time.Second || ttl > maxCapabilityTTL {
		return "", errors.New("inference capability TTL must be between one second and five minutes")
	}
	now := time.Now().UTC().Unix()
	expiresAt := now + int64(ttl/time.Second)
	if expiresAt <= now {
		return "", errors.New("inference capability TTL is invalid")
	}
	return sign(signingKey, claims{
		Version: capabilityVersion, Audience: capabilityAudience,
		TaskID: scope.TaskID, RunID: scope.RunID, Operation: scope.Operation,
		WorkerID: scope.WorkerID, AgentKey: scope.AgentKey, MachineID: scope.MachineID,
		IssuedAt: now, ExpiresAt: expiresAt,
	})
}

// Verify authenticates the token and its request task/run/operation scope.
// The authenticated worker identity is returned for database lease revalidation.
func Verify(signingKey, token, taskID, runID, operation string, now time.Time) (Scope, error) {
	var empty Scope
	signingKey = strings.TrimSpace(signingKey)
	taskID, runID, operation = strings.TrimSpace(taskID), strings.TrimSpace(runID), strings.TrimSpace(operation)
	if len(signingKey) < 32 || taskID == "" || runID == "" || operation == "" {
		return empty, errors.New("inference capability is invalid")
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != base64.RawURLEncoding.EncodedLen(sha256.Size) || len(parts[0]) > base64.RawURLEncoding.EncodedLen(maxPayloadBytes) {
		return empty, errors.New("inference capability is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 || len(payload) > maxPayloadBytes {
		return empty, errors.New("inference capability is invalid")
	}
	providedMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(providedMAC) != sha256.Size {
		return empty, errors.New("inference capability is invalid")
	}
	expectedMAC := signMAC(signingKey, parts[0])
	if !hmac.Equal(providedMAC, expectedMAC) {
		return empty, errors.New("inference capability is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	var value claims
	if decoder.Decode(&value) != nil {
		return empty, errors.New("inference capability is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return empty, errors.New("inference capability is invalid")
	}
	scope := normalizeScope(Scope{
		TaskID: value.TaskID, RunID: value.RunID, Operation: value.Operation,
		WorkerID: value.WorkerID, AgentKey: value.AgentKey, MachineID: value.MachineID,
	})
	if value.Version != capabilityVersion || value.Audience != capabilityAudience || !validScope(scope) ||
		scope.TaskID != taskID || scope.RunID != runID || scope.Operation != operation ||
		value.IssuedAt <= 0 || value.ExpiresAt <= value.IssuedAt || value.ExpiresAt-value.IssuedAt > int64(maxCapabilityTTL/time.Second) {
		return empty, errors.New("inference capability scope is invalid")
	}
	now = now.UTC()
	if now.Unix() >= value.ExpiresAt || now.Add(maxClockSkew).Unix() < value.IssuedAt {
		return empty, errors.New("inference capability is expired or not yet valid")
	}
	return scope, nil
}

func normalizeScope(scope Scope) Scope {
	scope.TaskID = strings.TrimSpace(scope.TaskID)
	scope.RunID = strings.TrimSpace(scope.RunID)
	scope.Operation = strings.TrimSpace(scope.Operation)
	scope.WorkerID = strings.TrimSpace(scope.WorkerID)
	scope.AgentKey = strings.TrimSpace(scope.AgentKey)
	scope.MachineID = strings.TrimSpace(scope.MachineID)
	return scope
}

func validScope(scope Scope) bool {
	workerID, workerErr := uuid.FromString(scope.WorkerID)
	machineID, machineErr := uuid.FromString(scope.MachineID)
	return scope.TaskID != "" && len(scope.TaskID) <= 64 && scope.RunID != "" && len(scope.RunID) <= 64 &&
		scope.Operation != "" && len(scope.Operation) <= 96 &&
		workerErr == nil && workerID != uuid.Nil && machineErr == nil && machineID != uuid.Nil &&
		agentKeyPattern.MatchString(scope.AgentKey)
}

func sign(signingKey string, value claims) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil || len(payload) == 0 || len(payload) > maxPayloadBytes {
		return "", errors.New("inference capability could not be encoded")
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := base64.RawURLEncoding.EncodeToString(signMAC(signingKey, encoded))
	return fmt.Sprintf("%s.%s", encoded, signature), nil
}

func signMAC(signingKey, encodedPayload string) []byte {
	// Domain separation prevents token signatures being reused as any other
	// control-plane HMAC, including signatures on immutable attempt policies.
	keyDerivation := hmac.New(sha256.New, []byte(signingKey))
	_, _ = keyDerivation.Write([]byte("itbem/inference-capability/signing-key/v2"))
	key := keyDerivation.Sum(nil)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("itbem/inference-capability/v2." + encodedPayload))
	return mac.Sum(nil)
}
