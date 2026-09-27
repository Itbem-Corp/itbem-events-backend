// Package agentcallbackauth implements request-bound signatures for callbacks
// sent by enrolled local agent machines. Private keys remain on the machine;
// the cloud stores only the corresponding public key.
package agentcallbackauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const (
	InstanceIDHeader = "X-ITBEM-Agent-Instance-ID"
	TimestampHeader  = "X-ITBEM-Agent-Timestamp"
	NonceHeader      = "X-ITBEM-Agent-Nonce"
	SignatureHeader  = "X-ITBEM-Agent-Signature"
	ProtocolVersion  = "ITBEM-AGENT-CALLBACK-V1"
	MaxClockSkew     = 90 * time.Second
)

var errInvalidCallbackSignature = errors.New("agent callback signature is invalid")

// SignRequest signs the exact request identity and raw body bytes. requestURI
// must be the origin-form value returned by URL.RequestURI(), including its
// raw query string. The signature is independent of JSON re-encoding.
func SignRequest(privateKey ed25519.PrivateKey, instanceID, method, requestURI string, timestamp int64, nonce string, body []byte) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errInvalidCallbackSignature
	}
	canonical, err := canonicalRequest(instanceID, method, requestURI, timestamp, nonce, body)
	if err != nil {
		return nil, errInvalidCallbackSignature
	}
	return ed25519.Sign(privateKey, canonical), nil
}

// VerifyRequest verifies a request signature and enforces a bounded timestamp
// window. Replay protection is intentionally separate and must be enforced by
// the server with a durable unique nonce record across all API instances.
func VerifyRequest(publicKey, signature []byte, instanceID, method, requestURI string, timestamp int64, nonce string, body []byte, now time.Time) error {
	canonical, err := canonicalRequest(instanceID, method, requestURI, timestamp, nonce, body)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return errInvalidCallbackSignature
	}
	requestTime := time.Unix(timestamp, 0).UTC()
	now = now.UTC()
	if timestamp <= 0 || requestTime.Before(now.Add(-MaxClockSkew)) || requestTime.After(now.Add(MaxClockSkew)) {
		return errInvalidCallbackSignature
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), canonical, signature) {
		return errInvalidCallbackSignature
	}
	return nil
}

// EncodePublicKey serializes a raw Ed25519 public key for operator-controlled
// registration. It is public material, not an authentication secret.
func EncodePublicKey(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errInvalidCallbackSignature
	}
	return base64.RawURLEncoding.EncodeToString(publicKey), nil
}

// DecodePublicKey accepts only the canonical unpadded base64url encoding of a
// raw 32-byte Ed25519 public key.
func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "=") {
		return nil, errInvalidCallbackSignature
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errInvalidCallbackSignature
	}
	return ed25519.PublicKey(decoded), nil
}

// EncodeSignature returns the canonical unpadded base64url header value.
func EncodeSignature(signature []byte) (string, error) {
	if len(signature) != ed25519.SignatureSize {
		return "", errInvalidCallbackSignature
	}
	return base64.RawURLEncoding.EncodeToString(signature), nil
}

// DecodeSignature rejects padded or non-canonical header encodings.
func DecodeSignature(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "=") {
		return nil, errInvalidCallbackSignature
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errInvalidCallbackSignature
	}
	return decoded, nil
}

// PublicKeyFingerprint is safe for display and audit logs; it never exposes a
// private key or a bearer credential.
func PublicKeyFingerprint(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errInvalidCallbackSignature
	}
	digest := sha256.Sum256(publicKey)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalRequest(instanceID, method, requestURI string, timestamp int64, nonce string, body []byte) ([]byte, error) {
	parsedInstanceID, err := uuid.FromString(strings.TrimSpace(instanceID))
	if err != nil || parsedInstanceID == uuid.Nil || timestamp <= 0 || strings.ContainsAny(instanceID+method+requestURI+nonce, "\r\n") {
		return nil, errInvalidCallbackSignature
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || len(method) > 16 || requestURI == "" || len(requestURI) > 8192 || !strings.HasPrefix(requestURI, "/") {
		return nil, errInvalidCallbackSignature
	}
	for _, character := range method {
		if character < 'A' || character > 'Z' {
			return nil, errInvalidCallbackSignature
		}
	}
	if _, err := url.ParseRequestURI(requestURI); err != nil {
		return nil, errInvalidCallbackSignature
	}
	parsedNonce, err := uuid.FromString(strings.TrimSpace(nonce))
	if err != nil || parsedNonce == uuid.Nil {
		return nil, errInvalidCallbackSignature
	}
	digest := sha256.Sum256(body)
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%d\n%s\n%s", ProtocolVersion, method, parsedInstanceID.String(), requestURI, timestamp, parsedNonce.String(), hex.EncodeToString(digest[:]))), nil
}
