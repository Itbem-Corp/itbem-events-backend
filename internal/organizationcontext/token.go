package organizationcontext

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/gofrs/uuid"
)

const purpose = "organization-context"

const (
	DefaultTTL = 5 * time.Minute
	MaxTTL     = 10 * time.Minute
	// MaxTokenLength bounds work performed on an untrusted request header.
	// Generated tokens are currently below 512 bytes.
	MaxTokenLength = 2048
)

type Claims struct {
	Purpose         string `json:"purpose"`
	Subject         string `json:"sub"`
	ApplicationCode string `json:"applicationCode"`
	OrganizationID  string `json:"organizationId"`
	ExpiresAt       int64  `json:"expiresAt"`
}

func Generate(subject, applicationCode string, organizationID uuid.UUID, ttl time.Duration) (string, time.Time, error) {
	secret := signingSecret()
	if secret == "" {
		return "", time.Time{}, errors.New("organization context secret is not configured")
	}
	subject = strings.TrimSpace(subject)
	applicationCode = normalize(applicationCode)
	if subject == "" {
		return "", time.Time{}, errors.New("organization context subject is required")
	}
	if applicationCode == "" {
		return "", time.Time{}, errors.New("organization context application code is required")
	}
	if organizationID == uuid.Nil {
		return "", time.Time{}, errors.New("organization context organization is required")
	}
	if ttl <= 0 || ttl > MaxTTL {
		return "", time.Time{}, errors.New("organization context ttl is outside the allowed range")
	}
	expiresAt := time.Now().UTC().Add(ttl)
	payload, err := json.Marshal(Claims{
		Purpose:         purpose,
		Subject:         subject,
		ApplicationCode: applicationCode,
		OrganizationID:  organizationID.String(),
		ExpiresAt:       expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + sign(encoded, secret), expiresAt, nil
}

func Validate(token, subject, applicationCode string, organizationID uuid.UUID) bool {
	token = strings.TrimSpace(token)
	subject = strings.TrimSpace(subject)
	applicationCode = normalize(applicationCode)
	if token == "" || len(token) > MaxTokenLength || subject == "" || applicationCode == "" || organizationID == uuid.Nil {
		return false
	}
	secret := signingSecret()
	payloadPart, signature, found := strings.Cut(token, ".")
	if secret == "" || !found || payloadPart == "" || len(signature) != sha256.Size*4/3+1 || strings.Contains(signature, ".") ||
		!hasValidSignature(payloadPart, signature, secret, previousSigningSecret()) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return false
	}
	var claims Claims
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	return claims.Purpose == purpose &&
		claims.Subject == subject &&
		claims.ApplicationCode == applicationCode &&
		claims.OrganizationID == organizationID.String() &&
		time.Now().Unix() <= claims.ExpiresAt
}

func hasValidSignature(payload, signature string, secrets ...string) bool {
	for _, secret := range secrets {
		if secret != "" && hmac.Equal([]byte(sign(payload, secret)), []byte(signature)) {
			return true
		}
	}
	return false
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func signingSecret() string {
	return strings.TrimSpace(os.Getenv("ORGANIZATION_CONTEXT_SECRET"))
}

func previousSigningSecret() string {
	return strings.TrimSpace(os.Getenv("ORGANIZATION_CONTEXT_SECRET_PREVIOUS"))
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
